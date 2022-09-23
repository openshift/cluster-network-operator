package allowlist

import (
	"context"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"log"
	"os"
	"time"

	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	allowlistDsName      = "cni-sysctl-allowlist-ds"
	allowlistAnnotation  = "app=cni-sysctl-allowlist-ds"
	manifestDir          = "../../bindata/allowlist/daemonset"
	allowlistManifestDir = "../../bindata/network/multus/004-sysctl-configmap.yaml"
)

func Add(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client) error {
	return add(mgr, newReconciler(mgr, status, c))
}

func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client) reconcile.Reconciler {
	return &ReconcileAllowlist{client: c, scheme: mgr.GetScheme(), status: status}
}

func add(mgr manager.Manager, r reconcile.Reconciler) error {
	c, err := controller.New("allowlist-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}
	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}
	return nil
}

var _ reconcile.Reconciler = &ReconcileAllowlist{}

type ReconcileAllowlist struct {
	client cnoclient.Client
	scheme *runtime.Scheme
	status *statusmanager.StatusManager
}

func (r *ReconcileAllowlist) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if exists, err := daemonsetConfigExists(ctx, r.client); !exists {
		err = createObjects(ctx, r.client, allowlistManifestDir)
		if err != nil {
			return reconcile.Result{}, errors.Wrapf(err, "error creating allowlist config map")
		}
	} else if err != nil {
		return reconcile.Result{}, errors.Wrapf(err, "error looking up allowlist config map")
	}

	if request.Namespace != names.MULTUS_NAMESPACE || request.Name != names.ALLOWLIST_CONFIG_NAME {
		return reconcile.Result{}, nil
	}

	configMap, err := getConfig(ctx, r.client, request.NamespacedName)
	if err != nil {
		return reconcile.Result{}, err
	}

	// No action to be taken if user deletes the config map. The sysctl's will stay unmodified until config map is recreated
	if configMap == nil {
		return reconcile.Result{}, nil
	}

	defer cleanup(ctx, r.client)

	// If daemonset still exists, delete it and reconcile again
	if daemonsetExists, err := daemonsetExists(ctx, r.client); daemonsetExists {
		return reconcile.Result{}, errors.New("daemonset already exist: deleting and retrying")
	} else if err != nil {
		return reconcile.Result{}, err
	}

	err = createObjects(ctx, r.client, manifestDir)
	if err != nil {
		return reconcile.Result{}, errors.Wrapf(err, "error creating allowlist daemonset")
	}

	err = checkDsPodsReady(ctx, r.client)
	if err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func createObjects(ctx context.Context, client cnoclient.Client, manifestDir string) error {
	data := render.MakeRenderData()
	data.Data["MultusImage"] = os.Getenv("MULTUS_IMAGE")
	data.Data["CniSysctlAllowlist"] = names.ALLOWLIST_CONFIG_NAME
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	manifests, err := render.RenderDir(manifestDir, &data)
	if err != nil {
		return err
	}
	for _, obj := range manifests {

		err = createObject(ctx, client, obj)
		if err != nil {
			return err
		}
	}
	return nil
}

func getConfig(ctx context.Context, client cnoclient.Client, namespacedName types.NamespacedName) (*corev1.ConfigMap, error) {
	configMap := &corev1.ConfigMap{}
	err := client.Default().CRClient().Get(ctx, namespacedName, configMap)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return configMap, nil
}

func createObject(ctx context.Context, client cnoclient.Client, obj *unstructured.Unstructured) error {
	err := client.Default().CRClient().Create(ctx, obj)
	if err != nil {
		return errors.Wrapf(err, "error creating %s %s/%s", obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName())
	}
	return nil
}

func checkDsPodsReady(ctx context.Context, client cnoclient.Client) error {
	err := wait.Poll(time.Second, time.Minute, func() (done bool, err error) {
		podList, err := client.Default().Kubernetes().CoreV1().Pods(names.MULTUS_NAMESPACE).List(
			ctx, metav1.ListOptions{LabelSelector: allowlistAnnotation})
		if err != nil {
			return false, err
		}
		for _, pod := range podList.Items {
			if !pod.Status.ContainerStatuses[0].Ready {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	return nil
}

func cleanup(ctx context.Context, client cnoclient.Client) {
	if exists, err := daemonsetExists(ctx, client); exists {
		err = deleteDeamonSet(ctx, client)
		if err != nil {
			log.Printf("Error cleaning up allow list daemonset: %+v", err)
		}
	} else if err != nil && !apierrors.IsNotFound(err) {
		log.Printf("Error looking up allowlist daemonset : %+v", err)
	}
}

func deleteDeamonSet(ctx context.Context, client cnoclient.Client) error {
	err := client.Default().Kubernetes().AppsV1().DaemonSets(names.MULTUS_NAMESPACE).Delete(
		ctx, allowlistDsName, metav1.DeleteOptions{})
	if err != nil {
		return err
	}
	return nil
}

func daemonsetExists(ctx context.Context, client cnoclient.Client) (bool, error) {
	ds, err := client.Default().Kubernetes().AppsV1().DaemonSets(names.MULTUS_NAMESPACE).Get(
		ctx, allowlistDsName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return ds != nil, nil
}

func daemonsetConfigExists(ctx context.Context, client cnoclient.Client) (bool, error) {
	cm, err := client.Default().Kubernetes().CoreV1().ConfigMaps(names.MULTUS_NAMESPACE).Get(
		ctx, names.ALLOWLIST_CONFIG_NAME, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return cm != nil, nil
}
