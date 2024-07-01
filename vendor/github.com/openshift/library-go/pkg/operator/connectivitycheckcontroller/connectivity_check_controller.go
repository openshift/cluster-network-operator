package connectivitycheckcontroller

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/api/operatorcontrolplane/v1alpha1"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	configv1listers "github.com/openshift/client-go/config/listers/config/v1"
	applyconfigv1alpha1 "github.com/openshift/client-go/operatorcontrolplane/applyconfigurations/operatorcontrolplane/v1alpha1"
	operatorcontrolplaneclient "github.com/openshift/client-go/operatorcontrolplane/clientset/versioned"
	operatorcontrolplaneinformers "github.com/openshift/client-go/operatorcontrolplane/informers/externalversions"
	listerv1alpha1 "github.com/openshift/client-go/operatorcontrolplane/listers/operatorcontrolplane/v1alpha1"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	kyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/klog/v2"

	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourcehelper"
	"github.com/openshift/library-go/pkg/operator/v1helpers"

	// this is dependency magnet, required so that we can sync the pod-network checker definition yaml
	_ "github.com/openshift/api/operatorcontrolplane/v1alpha1/zz_generated.crd-manifests"
)

const (
	managedByLabelKey   = "networking.openshift.io/managedBy"
	managedByLabelValue = "oc-connectivity-check-controller"
)

type ConnectivityCheckController interface {
	factory.Controller

	WithPodNetworkConnectivityCheckFn(podNetworkConnectivityCheckFn PodNetworkConnectivityCheckFunc) ConnectivityCheckController
	WithPodNetworkConnectivityCheckApplyFn(podNetworkConnectivityCheckApplyFn PodNetworkConnectivityCheckApplyFunc) ConnectivityCheckController
	WithReapOldConnectivityCheck(operatorcontrolplaneInformers operatorcontrolplaneinformers.SharedInformerFactory) ConnectivityCheckController
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
	checkLister                listerv1alpha1.PodNetworkConnectivityCheckNamespaceLister

	podNetworkConnectivityCheckFn      PodNetworkConnectivityCheckFunc
	podNetworkConnectivityCheckApplyFn PodNetworkConnectivityCheckApplyFunc

	enabledByDefault bool
}

type PodNetworkConnectivityCheckFunc func(ctx context.Context, syncContext factory.SyncContext) ([]*v1alpha1.PodNetworkConnectivityCheck, error)
type PodNetworkConnectivityCheckApplyFunc func(ctx context.Context, syncContext factory.SyncContext) ([]*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration, error)

func (c *connectivityCheckController) WithPodNetworkConnectivityCheckFn(podNetworkConnectivityCheckFn PodNetworkConnectivityCheckFunc) ConnectivityCheckController {
	c.podNetworkConnectivityCheckFn = podNetworkConnectivityCheckFn
	return c
}

func (c *connectivityCheckController) WithPodNetworkConnectivityCheckApplyFn(podNetworkConnectivityCheckApplyFn PodNetworkConnectivityCheckApplyFunc) ConnectivityCheckController {
	c.podNetworkConnectivityCheckApplyFn = podNetworkConnectivityCheckApplyFn
	return c
}

func (c *connectivityCheckController) WithReapOldConnectivityCheck(operatorcontrolplaneInformers operatorcontrolplaneinformers.SharedInformerFactory) ConnectivityCheckController {
	if operatorcontrolplaneInformers != nil {
		c.checkLister = operatorcontrolplaneInformers.Controlplane().V1alpha1().PodNetworkConnectivityChecks().Lister().PodNetworkConnectivityChecks(c.namespace)
	}
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

	var existingChecks []*v1alpha1.PodNetworkConnectivityCheck
	if c.checkLister != nil {
		existingChecks, err = c.checkLister.List(labels.Everything())
		if err != nil {
			return err
		}
	}

	var newCheckNames sets.Set[string]
	if c.podNetworkConnectivityCheckFn != nil {
		newCheckNames, err = c.handlePodNetworkConnectivityCheckFn(ctx, syncContext)
	} else if c.podNetworkConnectivityCheckApplyFn != nil {
		newCheckNames, err = c.handlePodNetworkConnectivityCheckApplyFn(ctx, syncContext)
	}
	if err != nil {
		return err
	}

	// TODO for checks which longer exist, mark them as completed

	// reap old connectivity checks
	for _, existingCheck := range existingChecks {
		if value, ok := existingCheck.Labels[managedByLabelKey]; !ok || value != managedByLabelValue || newCheckNames.Has(existingCheck.Name) {
			continue
		}
		err := c.operatorcontrolplaneClient.ControlplaneV1alpha1().PodNetworkConnectivityChecks(c.namespace).Delete(ctx, existingCheck.Name, metav1.DeleteOptions{})
		if err != nil {
			syncContext.Recorder().Eventf("EndpointCheckDeletionFailure", "%s: %v", resourcehelper.FormatResourceForCLIWithNamespace(existingCheck), err)
			continue
		}
		syncContext.Recorder().Eventf("EndpointCheckDeleted", "Deleted %s because it is no more valid.", resourcehelper.FormatResourceForCLIWithNamespace(existingCheck))
	}

	return nil
}

func (c *connectivityCheckController) handlePodNetworkConnectivityCheckFn(ctx context.Context, syncContext factory.SyncContext) (sets.Set[string], error) {
	newChecks, err := c.podNetworkConnectivityCheckFn(ctx, syncContext)
	if err != nil {
		return nil, err
	}
	pnccClient := c.operatorcontrolplaneClient.ControlplaneV1alpha1().PodNetworkConnectivityChecks(c.namespace)
	newCheckNames := sets.New[string]()
	for _, newCheck := range newChecks {
		newCheckNames.Insert(newCheck.Name)
		existing, err := pnccClient.Get(ctx, newCheck.Name, metav1.GetOptions{})
		if err == nil {
			if value, ok := existing.Labels[managedByLabelKey]; ok &&
				value == managedByLabelValue && equality.Semantic.DeepEqual(existing.Spec, newCheck.Spec) {
				// already exists, no changes, skip
				continue
			}
			updated := existing.DeepCopy()
			updated.Spec = *newCheck.Spec.DeepCopy()
			updated = setWithManagedByLabel(updated)
			_, err := pnccClient.Update(ctx, updated, metav1.UpdateOptions{})
			if err != nil {
				syncContext.Recorder().Warningf("EndpointDetectionFailure", "%s: %v", resourcehelper.FormatResourceForCLIWithNamespace(newCheck), err)
				continue
			}
			syncContext.Recorder().Eventf("EndpointCheckUpdated", "Updated %s because it changed.", resourcehelper.FormatResourceForCLIWithNamespace(newCheck))
			continue
		}
		if errors.IsNotFound(err) {
			newCheck = setWithManagedByLabel(newCheck)
			_, err = pnccClient.Create(ctx, newCheck, metav1.CreateOptions{})
		}
		if err != nil {
			syncContext.Recorder().Warningf("EndpointDetectionFailure", "%s: %v", resourcehelper.FormatResourceForCLIWithNamespace(newCheck), err)
			continue
		}
		syncContext.Recorder().Eventf("EndpointCheckCreated", "Created %s because it was missing.", resourcehelper.FormatResourceForCLIWithNamespace(newCheck))
	}
	return newCheckNames, nil
}

func (c *connectivityCheckController) handlePodNetworkConnectivityCheckApplyFn(ctx context.Context, syncContext factory.SyncContext) (sets.Set[string], error) {
	newChecks, err := c.podNetworkConnectivityCheckApplyFn(ctx, syncContext)
	if err != nil {
		return nil, err
	}
	pnccClient := c.operatorcontrolplaneClient.ControlplaneV1alpha1().PodNetworkConnectivityChecks(c.namespace)
	newCheckNames := sets.New[string]()
	for _, newCheck := range newChecks {
		newCheckNames.Insert(*newCheck.Name)
		newCheck.WithLabels(map[string]string{managedByLabelKey: managedByLabelValue})
		_, err := pnccClient.Apply(ctx, newCheck, metav1.ApplyOptions{
			Force:        true,
			FieldManager: c.Name(),
		})
		newCheckStrForCLI := FormatResourceForCLIWithNamespace(newCheck)
		if err != nil {
			syncContext.Recorder().Warningf("EndpointDetectionFailure", "%s: %v", newCheckStrForCLI, err)
			continue
		}
		syncContext.Recorder().Eventf("EndpointCheckApplied", "The check %s is applied", newCheckStrForCLI)
	}
	return newCheckNames, nil
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

func setWithManagedByLabel(check *v1alpha1.PodNetworkConnectivityCheck) *v1alpha1.PodNetworkConnectivityCheck {
	if check == nil {
		return check
	}
	if check.Labels == nil {
		check.Labels = make(map[string]string)
	}
	check.Labels[managedByLabelKey] = managedByLabelValue
	return check
}

func FormatResourceForCLIWithNamespace(pncc *applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration) string {
	return fmt.Sprintf("%s/%s -n %s", *pncc.Kind, *pncc.Name, *pncc.Namespace)
}
