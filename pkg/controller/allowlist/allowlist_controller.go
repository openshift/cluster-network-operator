// Package allowlist implements a Kubernetes controller that distributes CNI
// sysctl allowlist configuration to cluster nodes.
package allowlist

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	v1coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	dsName        = "cni-sysctl-allowlist-ds"
	dsAnnotation  = "app=cni-sysctl-allowlist-ds"
	dsManifestDir = "../../bindata/allowlist/daemonset"
	// Note: The default values come from default-cni-sysctl-allowlist which multus creates.
	defaultCMManifest = "../../bindata/network/multus/004-sysctl-configmap.yaml"
)

func Add(mgr manager.Manager, status *statusmanager.StatusManager, client cnoclient.Client, _ featuregates.FeatureGate) error {
	r:= &ReconcileAllowlist{client: client, status: status}
	c, err := controller.New("allowlist-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// watch for changes in all configmaps in our namespace
	cmInformer := v1coreinformers.NewConfigMapInformer(
		r.client.Default().Kubernetes(),
		names.MultusNamespace,
		0, // don't resync
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})

	r.client.Default().AddCustomInformer(cmInformer) // Tell the ClusterClient about this informer

	return c.Watch(&source.Informer{
		Informer: cmInformer,
		Handler:  &handler.EnqueueRequestForObject{},
		Predicates: []predicate.TypedPredicate[crclient.Object]{
			predicate.ResourceVersionChangedPredicate{},
			predicate.NewPredicateFuncs(func(object crclient.Object) bool {
				// Only care about cni-sysctl-allowlist, but also watching for default-cni-sysctl-allowlist
				// as a trigger for creating cni-sysctl-allowlist if it doesn't exist
				// NOTE: the cni-sysctl-allowlist is hardcoded in  pkg/network/multus.go:91
				return (strings.Contains(object.GetName(), names.AllowlistConfigName))
			}),
		},
	})
}

var _ reconcile.Reconciler = &ReconcileAllowlist{}

type ReconcileAllowlist struct {
	client cnoclient.Client
	status *statusmanager.StatusManager
}

func (r *ReconcileAllowlist) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	defer utilruntime.HandleCrash(r.status.SetDegradedOnPanicAndCrash)
	if exists, err := allowlistConfigMapExists(ctx, r.client); !exists {
		err = createObjectsFrom(ctx, r.client, defaultCMManifest)
		if err != nil {
			klog.Errorf("Failed to create allowlist config map: %v", err)
			return reconcile.Result{}, err
		}
	} else if err != nil {
		klog.Errorf("Failed to look up allowlist config map: %v", err)
		return reconcile.Result{}, err
	}

	if request.Name != names.AllowlistConfigName {
		return reconcile.Result{}, nil
	}
	klog.Infof("Reconcile allowlist for %s/%s", request.Namespace, request.Name)

	configMap, err := getConfigMap(ctx, r.client, request.NamespacedName)
	if err != nil {
		klog.Errorf("Failed to get config map: %v", err)
		return reconcile.Result{}, err
	}

	// Deletion handling: If user deletes the ConfigMap, we do nothing.
	// The allowlist file persists on nodes and pods continue working.
	// The auto-create check above will recreate the ConfigMap on next reconcile.
	// This prevents accidental deletion from breaking pod creation.
	// No action to be taken if user deletes the config map. The sysctl's will stay unmodified until config map is recreated
	if configMap == nil {
		return reconcile.Result{}, nil
	}

	defer cleanupDaemonSet(ctx, r.client)

	// If daemonset still exists, delete it and reconcile again
	ds, err := getDaemonSet(ctx, r.client)
	if err != nil {
		klog.Errorf("Failed to look up allowlist daemonset: %v", err)
		return reconcile.Result{}, err
	}
	if ds != nil {
		klog.Errorln("Allowlist daemonset already exists: deleting and retrying")
		return reconcile.Result{}, errors.New("retrying")
	}

	err = createObjectsFrom(ctx, r.client, dsManifestDir)
	if err != nil {
		klog.Errorf("Failed to create allowlist daemonset: %v", err)
		return reconcile.Result{}, err
	}

	// Do not retry when pods are not ready. The daemonset has a BestEffort QoS which
	// means that in some cases, the pods won't ever be scheduled.
	// This also prevents unwanted retries when one or more pods are not ready due to
	// issues with the cluster.
	// https://issues.redhat.com/browse/OCPBUGS-15818
	err = checkDsPodsReady(ctx, r.client)
	if err != nil {
		klog.Errorf("Failed to verify ready status on allowlist daemonset pods: %v", err)
		return reconcile.Result{}, nil
	}

	klog.Infoln("Successfully updated sysctl allowlist")
	return reconcile.Result{}, nil
}

func createObjectsFrom(ctx context.Context, client cnoclient.Client, manifestPath string) error {
	data := render.MakeRenderData()
	data.Data["MultusImage"] = os.Getenv("MULTUS_IMAGE")
	data.Data["CniSysctlAllowlist"] = names.AllowlistConfigName
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	manifests, err := render.RenderDir(manifestPath, &data)
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

func getConfigMap(ctx context.Context, client cnoclient.Client, namespacedName types.NamespacedName) (*corev1.ConfigMap, error) {
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
	return wait.PollUntilContextTimeout(ctx, time.Second, time.Minute, false, func(ctx context.Context) (done bool, err error) {
		ds, err := getDaemonSet(ctx, client)
		if err != nil {
			return false, err
		}
		if ds == nil || ds.GetUID() == "" {
			return false, fmt.Errorf("failed to get UID of daemon set")
		}

		podList, err := client.Default().Kubernetes().CoreV1().Pods(names.MultusNamespace).List(
			ctx, metav1.ListOptions{LabelSelector: dsAnnotation})
		if err != nil {
			return false, err
		}

		if len(podList.Items) == 0 {
			return false, nil
		}

		for _, pod := range podList.Items {
			// Ignore pods that are not owned by current daemon set.
			if len(pod.GetOwnerReferences()) == 0 || pod.GetOwnerReferences()[0].UID != ds.GetUID() {
				continue
			}

			if len(pod.Status.ContainerStatuses) == 0 || !pod.Status.ContainerStatuses[0].Ready {
				return false, nil
			}
		}
		return true, nil
	})
}

func cleanupDaemonSet(ctx context.Context, client cnoclient.Client) {
	ds, err := getDaemonSet(ctx, client)
	if err != nil {
		klog.Errorf("Error looking up allowlist daemonset : %+v", err)
		return
	}
	if ds != nil {
		err = deleteDaemonSet(ctx, client)
		if err != nil {
			klog.Errorf("Error cleaning up allow list daemonset: %+v", err)
		}
	}
}

func deleteDaemonSet(ctx context.Context, client cnoclient.Client) error {
	err := client.Default().Kubernetes().AppsV1().DaemonSets(names.MultusNamespace).Delete(
		ctx, dsName, metav1.DeleteOptions{})
	if err != nil {
		return err
	}
	return nil
}

func getDaemonSet(ctx context.Context, client cnoclient.Client) (*appsv1.DaemonSet, error) {
	ds, err := client.Default().Kubernetes().AppsV1().DaemonSets(names.MultusNamespace).Get(
		ctx, dsName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return ds, nil
}

func allowlistConfigMapExists(ctx context.Context, client cnoclient.Client) (bool, error) {
	cm, err := client.Default().Kubernetes().CoreV1().ConfigMaps(names.MultusNamespace).Get(
		ctx, names.AllowlistConfigName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return cm != nil, nil
}
