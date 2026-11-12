package allowlist

import (
	"context"
	"fmt"
	"os"
	"time"

	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// allowlistJobTTL is the time to keep finished Jobs before automatic cleanup (24 hours)
	allowlistJobTTL = 86400
	// allowlistJobActiveDeadline is the maximum time a Job can run before termination (10 minutes)
	allowlistJobActiveDeadline = 600
	// reconcilerID is the identifier prefix for log messages
	reconcilerID = "Allowlist node reconciler:"
)

var _ reconcile.Reconciler = &ReconcileNode{}

type ReconcileNode struct {
	client cnoclient.Client
	status *statusmanager.StatusManager
}

// AddNodeReconciler creates a new node reconciler and adds it to the manager.
// The node reconciler watches for node creation events and syncs the allowlist
// to all nodes when a new node joins the cluster.
func AddNodeReconciler(mgr manager.Manager, status *statusmanager.StatusManager, client cnoclient.Client, _ featuregates.FeatureGate) error {
	r := &ReconcileNode{client: client, status: status}
	c, err := controller.New("allowlist-node-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch when nodes are created.
	// When a new node joins the cluster, reconcile to deploy the allowlist file to the new node.
	return c.Watch(
		source.Kind[crclient.Object](
			mgr.GetCache(),
			&corev1.Node{},
			&handler.EnqueueRequestForObject{},
			nodePredicate(),
		),
	)
}

func (r *ReconcileNode) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	defer utilruntime.HandleCrash(r.status.SetDegradedOnPanicAndCrash)

	defaultCM := &corev1.ConfigMap{}
	if err := r.client.Default().CRClient().Get(ctx,
		types.NamespacedName{Name: names.DefaultAllowlistConfigName, Namespace: names.MultusNamespace},
		defaultCM); err != nil {
		klog.Infof("%s no default ConfigMap %v found", reconcilerID, err)
		return reconcile.Result{}, err
	}

	allowlistCM := &corev1.ConfigMap{}
	if err := r.client.Default().CRClient().Get(ctx,
		types.NamespacedName{Name: names.AllowlistConfigName, Namespace: names.MultusNamespace},
		allowlistCM); err != nil {
		return reconcile.Result{}, crclient.IgnoreNotFound(err)
	}

	// Skip job creation if allowlist matches default configuration.
	// The multus daemon already installs the default configmap on new nodes,
	// so we only need to run a job when the allowlist has been customized.
	if equality.Semantic.DeepEqual(allowlistCM.Data, defaultCM.Data) {
		klog.Infof("%s ConfigMaps are identical, skipping job creation for node %s", reconcilerID, request.Name)
		return reconcile.Result{}, nil
	}

	nodeName := request.Name

	// Job name includes ConfigMap ResourceVersion to ensure old jobs with stale
	// configs aren't reused when the allowlist is updated.
	job := newAllowlistJobFor(nodeName, allowlistCM.ResourceVersion)
	createErr := r.client.Default().CRClient().Create(ctx, job)

	// Handle creation errors (excluding AlreadyExists)
	if createErr != nil && !apierrors.IsAlreadyExists(createErr) {
		klog.Infof("%s failed to create job %s: %v", reconcilerID, job.Name, createErr)
		return reconcile.Result{}, createErr
	}

	// Job created successfully - requeue to check status later
	if createErr == nil {
		klog.Infof("%s job %s created", reconcilerID, job.Name)
		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Job already exists - fetch it to check status immediately
	if err := r.client.Default().CRClient().Get(ctx,
		types.NamespacedName{Name: job.Name, Namespace: names.MultusNamespace}, job); err != nil {
		klog.Infof("%s failed to get existing job %s: %v", reconcilerID, job.Name, err)
		return reconcile.Result{}, err
	}

	// Check job status
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
			klog.Infof("%s job %s completed successfully, cleaning up", reconcilerID, job.Name)
			err := r.client.Default().CRClient().Delete(ctx, job,
				crclient.PropagationPolicy(metav1.DeletePropagationBackground))
			return reconcile.Result{}, crclient.IgnoreNotFound(err)
		}
		if (cond.Type == batchv1.JobFailureTarget || cond.Type == batchv1.JobFailed) &&
			cond.Status == corev1.ConditionTrue {
			klog.Infof("%s job %s failed: %s (preserved for debugging, TTL cleanup in 24h)",
				reconcilerID, job.Name, cond.Reason)
			return reconcile.Result{}, nil
		}
	}

	klog.Infof("%s job %s is in progress", reconcilerID, job.Name)

	return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
}

// nodePredicate returns a predicate that filters Node events.
// Only node creations trigger reconciliation to distribute config to new nodes.
func nodePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			return true
		},
		UpdateFunc: func(_ event.UpdateEvent) bool {
			return false
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return false
		},
	}
}

func newAllowlistJobFor(nodeName string, configMapVersion string) *batchv1.Job {
	jobName := fmt.Sprintf("cni-sysctl-allowlist-%.32s-%.8s", nodeName, configMapVersion)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: names.MultusNamespace,
			Labels: map[string]string{
				"app":  "cni-sysctl-allowlist-job",
				"node": nodeName,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To(int32(3)),
			TTLSecondsAfterFinished: ptr.To(int32(allowlistJobTTL)),
			ActiveDeadlineSeconds:   ptr.To(int64(allowlistJobActiveDeadline)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":  "cni-sysctl-allowlist-job",
						"node": nodeName,
					},
					Annotations: map[string]string{
						"target.workload.openshift.io/management": `{"effect": "PreferredDuringScheduling"}`,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:     corev1.RestartPolicyNever,
					PriorityClassName: "openshift-user-critical",
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": nodeName,
					},
					Containers: []corev1.Container{
						{
							Name:    "kube-multus-additional-cni-plugins",
							Image:   os.Getenv("MULTUS_IMAGE"),
							Command: []string{"/bin/bash", "-c", "cp /entrypoint/allowlist.conf /host/etc/cni/tuning/"},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("10m"),
									corev1.ResourceMemory: resource.MustParse("10Mi"),
								},
							},
							SecurityContext: &corev1.SecurityContext{
								Privileged: ptr.To(true),
							},
							TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "cni-sysctl-allowlist",
									MountPath: "/entrypoint",
								},
								{
									Name:      "tuning-conf-dir",
									MountPath: "/host/etc/cni/tuning/",
									ReadOnly:  false,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "cni-sysctl-allowlist",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: names.AllowlistConfigName,
									},
									DefaultMode: ptr.To(int32(0644)),
								},
							},
						},
						{
							Name: "tuning-conf-dir",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/etc/cni/tuning/",
									Type: ptr.To(corev1.HostPathDirectoryOrCreate),
								},
							},
						},
					},
				},
			},
		},
	}
}
