package connectivitycheckcontroller

import (
	"context"
	"encoding/json"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/api/operatorcontrolplane/v1alpha1"
	operatorcontrolplaneclient "github.com/openshift/client-go/operatorcontrolplane/clientset/versioned"
	"github.com/openshift/library-go/pkg/operator/connectivitycheckcontroller/bindata"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/klog/v2"

	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourcehelper"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
)

type ConnectivityCheckController interface {
	factory.Controller

	WithPodNetworkConnectivityCheckFn(podNetworkConnectivityCheckFn PodNetworkConnectivityCheckFunc) ConnectivityCheckController
}

func NewConnectivityCheckController(
	namespace string,
	operatorClient v1helpers.OperatorClient,
	operatorcontrolplaneClient *operatorcontrolplaneclient.Clientset,
	apiextensionsClient *apiextensionsclient.Clientset,
	apiextensionsInformers apiextensionsinformers.SharedInformerFactory,
	triggers []factory.Informer,
	recorder events.Recorder,
	enabledByDefault bool,
) ConnectivityCheckController {
	c := &connectivityCheckController{
		namespace:                  namespace,
		operatorClient:             operatorClient,
		operatorcontrolplaneClient: operatorcontrolplaneClient,
		apiextensionsClient:        apiextensionsClient,
		enabledByDefault:           enabledByDefault,
	}

	allTriggers := []factory.Informer{
		operatorClient.Informer(),
		apiextensionsInformers.Apiextensions().V1().CustomResourceDefinitions().Informer(),
	}
	allTriggers = append(allTriggers, triggers...)

	c.Controller = factory.New().
		WithSync(c.Sync).
		WithInformers(allTriggers...).
		ToController("ConnectivityCheckController", recorder.WithComponentSuffix("connectivity-check-controller"))
	return c
}

type connectivityCheckController struct {
	factory.Controller
	namespace                  string
	operatorClient             v1helpers.OperatorClient
	operatorcontrolplaneClient *operatorcontrolplaneclient.Clientset
	apiextensionsClient        *apiextensionsclient.Clientset

	podNetworkConnectivityCheckFn PodNetworkConnectivityCheckFunc

	enabledByDefault bool
}

type PodNetworkConnectivityCheckFunc func(ctx context.Context, syncContext factory.SyncContext) ([]*v1alpha1.PodNetworkConnectivityCheck, error)

func (c *connectivityCheckController) WithPodNetworkConnectivityCheckFn(podNetworkConnectivityCheckFn PodNetworkConnectivityCheckFunc) ConnectivityCheckController {
	c.podNetworkConnectivityCheckFn = podNetworkConnectivityCheckFn
	return c
}

// unsupportedConfigOverrides is a partial struct to deserialize just the parts of
// spec.unsupportedConfigOverrides that we are interested in.
type unsupportedConfigOverrides struct {
	Operator struct {
		EnableConnectivityCheckController string `json:"enableConnectivityCheckController"`
	} `json:"operator"`
}

const podnetworkconnectivitychecksCRDName = "podnetworkconnectivitychecks.controlplane.operator.openshift.io"

func (c *connectivityCheckController) Sync(ctx context.Context, syncContext factory.SyncContext) error {
	operatorSpec, _, _, err := c.operatorClient.GetOperatorState()
	if err != nil {
		return err
	}
	switch operatorSpec.ManagementState {
	case operatorv1.Managed:
	case operatorv1.Unmanaged:
		return nil
	case operatorv1.Removed:
		return nil
	default:
		syncContext.Recorder().Warningf("ManagementStateUnknown", "Unrecognized operator management state %q", operatorSpec.ManagementState)
		return nil
	}

	// is this controller enabled?
	enabled, err := c.enabled(operatorSpec)
	if err != nil {
		return err
	}

	crd, getCRDErr := c.apiextensionsClient.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, podnetworkconnectivitychecksCRDName, metav1.GetOptions{})
	if getCRDErr != nil && !errors.IsNotFound(getCRDErr) {
		return getCRDErr
	}

	if !enabled {
		// controller is not enabled
		if errors.IsNotFound(getCRDErr) {
			// crd has already been removed
			return nil
		}

		// delete all podnetworkconnectivitychecks managed by this controller instance
		checks, err := c.operatorcontrolplaneClient.ControlplaneV1alpha1().PodNetworkConnectivityChecks(c.namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		for _, check := range checks.Items {
			err := c.operatorcontrolplaneClient.ControlplaneV1alpha1().PodNetworkConnectivityChecks(c.namespace).Delete(ctx, check.Name, metav1.DeleteOptions{})
			if err != nil {
				return err
			}
		}

		// if there are still podnetworkconnectivitychecks resources in other namespaces, do not delete the crd
		checks, err = c.operatorcontrolplaneClient.ControlplaneV1alpha1().PodNetworkConnectivityChecks("").List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		if len(checks.Items) > 0 {
			return nil
		}

		// if podnetworkconnectivitycheck crd is less than 2 minutes old, don't delete the crd yet. This allows another
		// instance that has been enabled up to 2 minutes to create its podnetworkconnectivitycheck resources.
		if crd.CreationTimestamp.Time.After(time.Now().Add(-2 * time.Minute)) {
			return nil
		}

		// delete the podnetworkconnectivitycheck crd that should not exist
		return c.apiextensionsClient.ApiextensionsV1().CustomResourceDefinitions().Delete(ctx, crd.Name, metav1.DeleteOptions{})
	}

	// controller is enabled
	if errors.IsNotFound(getCRDErr) {
		// create the podnetworkconnectivitycheck crd that should exist
		applyResults := resourceapply.ApplyDirectly(
			resourceapply.NewClientHolder().WithAPIExtensionsClient(c.apiextensionsClient),
			syncContext.Recorder(),
			func(name string) ([]byte, error) { return bindata.Asset(name) },
			"pkg/operator/connectivitycheckcontroller/manifests/controlplane.operator.openshift.io_podnetworkconnectivitychecks.yaml",
		)
		if applyResults[0].Error != nil {
			return applyResults[0].Error
		}
	}

	checks, err := c.podNetworkConnectivityCheckFn(ctx, syncContext)
	if err != nil {
		return err
	}

	pnccClient := c.operatorcontrolplaneClient.ControlplaneV1alpha1().PodNetworkConnectivityChecks(c.namespace)
	for _, check := range checks {
		existing, err := pnccClient.Get(ctx, check.Name, metav1.GetOptions{})
		if err == nil {
			if equality.Semantic.DeepEqual(existing.Spec, check.Spec) {
				// already exists, no changes, skip
				continue
			}
			updated := existing.DeepCopy()
			updated.Spec = *check.Spec.DeepCopy()
			_, err := pnccClient.Update(ctx, updated, metav1.UpdateOptions{})
			if err != nil {
				syncContext.Recorder().Warningf("EndpointDetectionFailure", "%s: %v", resourcehelper.FormatResourceForCLIWithNamespace(check), err)
				continue
			}
			syncContext.Recorder().Eventf("EndpointCheckUpdated", "Updated %s because it changed.", resourcehelper.FormatResourceForCLIWithNamespace(check))
		}
		if errors.IsNotFound(err) {
			_, err = pnccClient.Create(ctx, check, metav1.CreateOptions{})
		}
		if err != nil {
			syncContext.Recorder().Warningf("EndpointDetectionFailure", "%s: %v", resourcehelper.FormatResourceForCLIWithNamespace(check), err)
			continue
		}
		syncContext.Recorder().Eventf("EndpointCheckCreated", "Created %s because it was missing.", resourcehelper.FormatResourceForCLIWithNamespace(check))
	}

	// TODO for checks which longer exist, mark them as completed

	// TODO reap old connectivity checks

	return nil
}

func (c *connectivityCheckController) enabled(operatorSpec *operatorv1.OperatorSpec) (bool, error) {
	overrides := unsupportedConfigOverrides{}
	if raw := operatorSpec.UnsupportedConfigOverrides.Raw; len(raw) > 0 {
		jsonRaw, err := kyaml.ToJSON(raw)
		if err != nil {
			klog.Warning(err)
			jsonRaw = raw
		}
		if err := json.Unmarshal(jsonRaw, &overrides); err != nil {
			return false, err
		}
	}
	switch {
	case c.enabledByDefault && overrides.Operator.EnableConnectivityCheckController == "False":
		klog.V(3).Info("ConnectivityCheckController is disabled as requested by an unsupported configuration option.")
		return false, nil
	case !c.enabledByDefault && overrides.Operator.EnableConnectivityCheckController == "True":
		klog.V(3).Info("ConnectivityCheckController is enabled as requested by an unsupported configuration option.")
		return true, nil
	case c.enabledByDefault:
		klog.V(3).Info("ConnectivityCheckController is enabled by default.")
		return true, nil
	default:
		klog.V(3).Info("ConnectivityCheckController is disabled by default.")
		return false, nil
	}
}
