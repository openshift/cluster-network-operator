package cronjob_scheduler

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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
	manifestDir              = "bindata/cronjob-scheduler/daemonset"
	cronConfigmapDsName      = "whereabouts-reconciler-scheduler-configmap-ds"
	cronConfigmapManifestDir = "bindata/cronjob-scheduler/config/configmap.yaml"
	cronAnnotation           = "app=whereabouts-reconciler-scheduler-configmap-ds"
)

func Add(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client) error {
	return add(mgr, newReconciler(mgr, status, c))
}

func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client) *ReconcileCronjobScheduler {
	return &ReconcileCronjobScheduler{client: c, scheme: mgr.GetScheme(), status: status}
}

func add(mgr manager.Manager, r *ReconcileCronjobScheduler) error {
	c, err := controller.New("cronjob-scheduler-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// watch for changes in all configmaps in our namespace
	cmInformer := v1coreinformers.NewConfigMapInformer(
		r.client.Default().Kubernetes(),
		names.MULTUS_NAMESPACE,
		0, // don't resync
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})

	r.client.Default().AddCustomInformer(cmInformer) // Tell the ClusterClient about this informer

	return c.Watch(&source.Informer{Informer: cmInformer},
		&handler.EnqueueRequestForObject{},
		predicate.ResourceVersionChangedPredicate{},
		predicate.NewPredicateFuncs(func(object crclient.Object) bool {
			return (strings.Contains(object.GetName(), names.CRON_SCHEDULER_CONFIG_NAME))
		}),
	)
}

type ReconcileCronjobScheduler struct {
	client cnoclient.Client
	scheme *runtime.Scheme
	status *statusmanager.StatusManager
}

func (r *ReconcileCronjobScheduler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	defer utilruntime.HandleCrash(r.status.SetDegradedOnPanicAndCrash)

	log.Printf("Reconciling Network.operator.openshift.io %s\n", request.Name)

	if exists, err := daemonsetConfigExists(ctx, r.client); !exists {
		err = createObjects(ctx, r.client, cronConfigmapManifestDir)
		if err != nil {
			klog.Errorf("Failed to create cronjob scheduler config map: %v", err)
			return reconcile.Result{}, err
		}
	} else if err != nil {
		klog.Errorf("Failed to look up cronjob scheduler config map: %v", err)
		return reconcile.Result{}, err
	}

	if request.Namespace != names.MULTUS_NAMESPACE || request.Name != names.CRON_SCHEDULER_CONFIG_NAME {
		return reconcile.Result{}, nil
	}
	klog.Infof("Reconcile cronjob scheduler for %s/%s", request.Namespace, request.Name)

	configMap, err := getConfig(ctx, r.client, request.NamespacedName)
	if err != nil {
		klog.Errorf("Failed to get config map: %v", err)
		return reconcile.Result{}, err
	}

	// No action to be taken if user deletes the config map. The reconciler_cron_file value will stay unmodified until config map is recreated
	if configMap == nil {
		return reconcile.Result{}, nil
	}

	defer cleanup(ctx, r.client)

	// If daemonset still exists, delete it and reconcile again
	ds, err := getDaemonSet(ctx, r.client)
	if err != nil {
		klog.Errorf("Failed to look up cronjob scheduler daemonset: %v", err)
		return reconcile.Result{}, err
	}
	if ds != nil {
		klog.Errorln("Cronjob scheduler daemonset already exists: deleting and retrying")
		return reconcile.Result{}, errors.New("retrying")
	}

	err = createObjects(ctx, r.client, manifestDir)
	if err != nil {
		klog.Errorf("Failed to create cronjob scheduler daemonset: %v", err)
		return reconcile.Result{}, err
	}

	// Do not retry when pods are not ready. The daemonset has a BestEffort QoS which
	// means that in some cases, the pods won't ever be scheduled.
	// This also prevents unwanted retries when one or more pods are not ready due to
	// issues with the cluster.
	// https://issues.redhat.com/browse/OCPBUGS-15818
	err = checkDsPodsReady(ctx, r.client)
	if err != nil {
		klog.Errorf("Failed to verify ready status on cronjob scheduler daemonset pods: %v", err)
		return reconcile.Result{}, nil
	}

	klog.Infoln("Successfully updated cronjob scheduler configmap")
	return reconcile.Result{}, nil
}

func createObjects(ctx context.Context, client cnoclient.Client, manifestDir string) error {
	data := render.MakeRenderData()
	data.Data["MultusImage"] = os.Getenv("MULTUS_IMAGE")
	data.Data["CronSchedulerConfigmap"] = names.CRON_SCHEDULER_CONFIG_NAME
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["UserEditableCronSchedulerConfigmap"] = names.USER_EDITABLE_CRON_SCHEDULER_CONFIGMAP
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
	return wait.PollUntilContextTimeout(ctx, time.Second, time.Minute, false, func(ctx context.Context) (done bool, err error) {
		ds, err := getDaemonSet(ctx, client)
		if err != nil {
			return false, err
		}
		if ds == nil || ds.GetUID() == "" {
			return false, fmt.Errorf("failed to get UID of daemon set")
		}

		podList, err := client.Default().Kubernetes().CoreV1().Pods(names.MULTUS_NAMESPACE).List(
			ctx, metav1.ListOptions{LabelSelector: cronAnnotation})
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

func cleanup(ctx context.Context, client cnoclient.Client) {
	ds, err := getDaemonSet(ctx, client)
	if err != nil {
		klog.Errorf("Error looking up cron scheduler daemonset : %+v", err)
		return
	}
	if ds != nil {
		err = deleteDaemonSet(ctx, client)
		if err != nil {
			klog.Errorf("Error cleaning up cron scheduler list daemonset: %+v", err)
		}
	}
}

func deleteDaemonSet(ctx context.Context, client cnoclient.Client) error {
	err := client.Default().Kubernetes().AppsV1().DaemonSets(names.MULTUS_NAMESPACE).Delete(
		ctx, cronConfigmapDsName, metav1.DeleteOptions{})
	if err != nil {
		return err
	}
	return nil
}

func getDaemonSet(ctx context.Context, client cnoclient.Client) (*appsv1.DaemonSet, error) {
	ds, err := client.Default().Kubernetes().AppsV1().DaemonSets(names.MULTUS_NAMESPACE).Get(
		ctx, cronConfigmapDsName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return ds, nil
}

func daemonsetConfigExists(ctx context.Context, client cnoclient.Client) (bool, error) {
	cm, err := client.Default().Kubernetes().CoreV1().ConfigMaps(names.MULTUS_NAMESPACE).Get(
		ctx, names.CRON_SCHEDULER_CONFIG_NAME, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return cm != nil, nil
}
