package connectivitycheckcontroller

import (
	"context"
	"embed"
	"encoding/json"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/api/operatorcontrolplane/v1alpha1"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	configv1listers "github.com/openshift/client-go/config/listers/config/v1"
	operatorcontrolplaneclient "github.com/openshift/client-go/operatorcontrolplane/clientset/versioned"
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
	configInformers configinformers.SharedInformerFactory,
	triggers []factory.Informer,
	recorder events.Recorder,
	enabledByDefault bool,
) ConnectivityCheckController {
	c := &connectivityCheckController{
		namespace:                  namespace,
		operatorClient:             operatorClient,
		operatorcontrolplaneClient: operatorcontrolplaneClient,
		apiextensionsClient:        apiextensionsClient,
		clusterVersionLister:       configInformers.Config().V1().ClusterVersions().Lister(),
		enabledByDefault:           enabledByDefault,
	}

	allTriggers := []factory.Informer{
		operatorClient.Informer(),
		apiextensionsInformers.Apiextensions().V1().CustomResourceDefinitions().Informer(),
		configInformers.Config().V1().ClusterVersions().Informer(),
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
	clusterVersionLister       configv1listers.ClusterVersionLister

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

	// do nothing while an upgrade is in progress
	clusterVersion, err := c.clusterVersionLister.Get("version")
	if err != nil {
		return err
	}
	desired := clusterVersion.Status.Desired.Version
	history := clusterVersion.Status.History
	// upgrade is in progress if there is no history, or the latest history entry matches the desired version and is not completed
	if len(history) == 0 {
		klog.V(1).Infof("ConnectivityCheckController is waiting for transition to first desired version (%s) to be completed.", desired)
		return nil
	}
	if history[0].Version != desired {
		klog.V(1).Infof("ConnectivityCheckController is waiting for transition to desired version (%s) to be started.", desired)
		return nil
	}
	if history[0].State != configv1.CompletedUpdate {
		klog.V(1).Infof("ConnectivityCheckController is waiting for transition to desired version (%s) to be completed.", desired)
		return nil
	}

	// re-create crd if deleted during an upgrade
	err = ensureConnectivityCheckCRDExists(ctx, syncContext, c.apiextensionsClient)
	if err != nil {
		return err
	}

	if !enabled {
		// controller is not enabled, delete all podnetworkconnectivitychecks managed by this controller instance
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
		return nil
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

//go:embed manifests
var assets embed.FS

func ensureConnectivityCheckCRDExists(ctx context.Context, syncContext factory.SyncContext, client *apiextensionsclient.Clientset) error {
	_, err := client.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, podnetworkconnectivitychecksCRDName, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if errors.IsNotFound(err) {
		// create the podnetworkconnectivitycheck crd that should exist
		applyResults := resourceapply.ApplyDirectly(
			ctx,
			resourceapply.NewClientHolder().WithAPIExtensionsClient(client),
			syncContext.Recorder(),
			nil,
			assets.ReadFile,
			"manifests/controlplane.operator.openshift.io_podnetworkconnectivitychecks.yaml",
		)
		if applyResults[0].Error != nil {
			return applyResults[0].Error
		}
	}
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
