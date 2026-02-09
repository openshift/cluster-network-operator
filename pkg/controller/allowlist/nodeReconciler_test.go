package allowlist

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	node                 = "new-node-x"
	defaultCMData        = "default-allowlist-content"
	testConfigMapVersion = "12345"
)

func TestMain(m *testing.M) {
	// suppresses klog output during tests
	klog.SetLogger(logr.Discard())
	os.Exit(m.Run())
}

func TestReconcileNode(t *testing.T) {
	tests := map[string]struct {
		existingObjects []crclient.Object
		wantErr         error
		wantResult      reconcile.Result
		wantJob         *batchv1.Job
	}{
		"no default ConfigMap returns error": {
			existingObjects: []crclient.Object{},
			wantErr: apierrors.NewNotFound(
				schema.GroupResource{Resource: "configmaps"},
				names.DefaultAllowlistConfigName,
			),
			wantResult: reconcile.Result{},
			wantJob:    nil,
		},
		"identical ConfigMaps skip job creation": {
			existingObjects: []crclient.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      names.DefaultAllowlistConfigName,
						Namespace: names.MultusNamespace,
					},
					Data: map[string]string{
						"allowlist.conf": defaultCMData,
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:            names.AllowlistConfigName,
						Namespace:       names.MultusNamespace,
						ResourceVersion: testConfigMapVersion,
					},
					Data: map[string]string{
						"allowlist.conf": defaultCMData,
					},
				},
			},
			wantErr:    nil,
			wantResult: reconcile.Result{},
			wantJob:    nil,
		},
		"different ConfigMaps create job": {
			existingObjects: []crclient.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      names.DefaultAllowlistConfigName,
						Namespace: names.MultusNamespace,
					},
					Data: map[string]string{
						"allowlist.conf": defaultCMData,
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:            names.AllowlistConfigName,
						Namespace:       names.MultusNamespace,
						ResourceVersion: testConfigMapVersion,
					},
					Data: map[string]string{
						"allowlist.conf": defaultCMData + "delta",
					},
				},
			},
			wantErr:    nil,
			wantResult: reconcile.Result{RequeueAfter: 30 * time.Second},
			wantJob:    newAllowlistJobFor(node, testConfigMapVersion),
		},
		"succeeded job is deleted": {
			existingObjects: []crclient.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      names.DefaultAllowlistConfigName,
						Namespace: names.MultusNamespace,
					},
					Data: map[string]string{
						"allowlist.conf": defaultCMData,
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:            names.AllowlistConfigName,
						Namespace:       names.MultusNamespace,
						ResourceVersion: testConfigMapVersion,
					},
					Data: map[string]string{
						"allowlist.conf": defaultCMData + "delta",
					},
				},
				func() *batchv1.Job {
					job := newAllowlistJobFor(node, testConfigMapVersion)
					job.Status.Succeeded = 1
					job.Status.Conditions = []batchv1.JobCondition{
						{
							Type:   batchv1.JobComplete,
							Status: corev1.ConditionTrue,
						},
					}
					return job
				}(),
			},
			wantErr:    nil,
			wantResult: reconcile.Result{},
			wantJob:    &batchv1.Job{},
		},
		"failed job with BackoffLimitExceeded is preserved": {
			existingObjects: []crclient.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      names.DefaultAllowlistConfigName,
						Namespace: names.MultusNamespace,
					},
					Data: map[string]string{
						"allowlist.conf": defaultCMData,
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:            names.AllowlistConfigName,
						Namespace:       names.MultusNamespace,
						ResourceVersion: testConfigMapVersion,
					},
					Data: map[string]string{
						"allowlist.conf": defaultCMData + "delta",
					},
				},
				func() *batchv1.Job {
					job := newAllowlistJobFor(node, testConfigMapVersion)
					job.Status.Failed = 3
					job.Status.Conditions = []batchv1.JobCondition{
						{
							Type:   batchv1.JobFailed,
							Status: corev1.ConditionTrue,
							Reason: "BackoffLimitExceeded",
						},
					}
					return job
				}(),
			},
			wantErr:    nil,
			wantResult: reconcile.Result{},
			wantJob: func() *batchv1.Job {
				job := newAllowlistJobFor(node, testConfigMapVersion)
				job.Status.Failed = 3
				job.Status.Conditions = []batchv1.JobCondition{
					{
						Type:   batchv1.JobFailed,
						Status: corev1.ConditionTrue,
						Reason: "BackoffLimitExceeded",
					},
				}
				return job
			}(),
		},
		"failed job with DeadlineExceeded is preserved": {
			existingObjects: []crclient.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      names.DefaultAllowlistConfigName,
						Namespace: names.MultusNamespace,
					},
					Data: map[string]string{
						"allowlist.conf": defaultCMData,
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:            names.AllowlistConfigName,
						Namespace:       names.MultusNamespace,
						ResourceVersion: testConfigMapVersion,
					},
					Data: map[string]string{
						"allowlist.conf": defaultCMData + "delta",
					},
				},
				func() *batchv1.Job {
					job := newAllowlistJobFor(node, testConfigMapVersion)
					job.Status.Failed = 0
					job.Status.Conditions = []batchv1.JobCondition{
						{
							Type:   batchv1.JobFailed,
							Status: corev1.ConditionTrue,
							Reason: "DeadlineExceeded",
						},
					}
					return job
				}(),
			},
			wantErr:    nil,
			wantResult: reconcile.Result{},
			wantJob: func() *batchv1.Job {
				job := newAllowlistJobFor(node, testConfigMapVersion)
				job.Status.Failed = 0
				job.Status.Conditions = []batchv1.JobCondition{
					{
						Type:   batchv1.JobFailed,
						Status: corev1.ConditionTrue,
						Reason: "DeadlineExceeded",
					},
				}
				return job
			}(),
		},
		"active job requeues for status check": {
			existingObjects: []crclient.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      names.DefaultAllowlistConfigName,
						Namespace: names.MultusNamespace,
					},
					Data: map[string]string{
						"allowlist.conf": defaultCMData,
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:            names.AllowlistConfigName,
						Namespace:       names.MultusNamespace,
						ResourceVersion: testConfigMapVersion,
					},
					Data: map[string]string{
						"allowlist.conf": defaultCMData + "delta",
					},
				},
				func() *batchv1.Job {
					job := newAllowlistJobFor(node, testConfigMapVersion)
					job.Status.Active = 1
					return job
				}(),
			},
			wantErr:    nil,
			wantResult: reconcile.Result{RequeueAfter: 30 * time.Second},
			wantJob: func() *batchv1.Job {
				job := newAllowlistJobFor(node, testConfigMapVersion)
				job.Status.Active = 1
				return job
			}(),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewFakeClient(tc.existingObjects...)

			r := &ReconcileNode{
				client: client,
				status: statusmanager.New(client, "testing", names.StandAloneClusterName),
			}

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      node,
					Namespace: names.MultusNamespace,
				},
			}

			result, err := r.Reconcile(t.Context(), req)
			if diff := cmp.Diff(tc.wantErr, err); diff != "" {
				t.Fatalf("error mismatch (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantResult, result); diff != "" {
				t.Errorf("result mismatch (-want +got):\n%s", diff)
			}

			if tc.wantErr != nil || tc.wantJob == nil {
				return
			}

			gotJob := &batchv1.Job{}
			err = client.Default().CRClient().Get(t.Context(),
				types.NamespacedName{
					Name:      fmt.Sprintf("cni-sysctl-allowlist-%s-%s", node, testConfigMapVersion),
					Namespace: names.MultusNamespace,
				}, gotJob)
			if err != nil && tc.wantJob != nil && tc.wantJob.Name != "" {
				t.Fatalf("unexpected error getting job: %v", err)
			}

			opts := []cmp.Option{
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "Generation", "ManagedFields"),
			}
			if diff := cmp.Diff(tc.wantJob, gotJob, opts...); diff != "" {
				t.Errorf("Job spec mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestNodePredicate(t *testing.T) {
	predicate := nodePredicate()

	tests := map[string]struct {
		event any
		want  bool
	}{
		"CreateFunc with node": {
			event: event.CreateEvent{
				Object: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: node,
					},
				},
			},
			want: true,
		},
		"UpdateFunc with node": {
			event: event.UpdateEvent{
				ObjectOld: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:            node,
						ResourceVersion: "1",
					},
				},
				ObjectNew: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:            node,
						ResourceVersion: "2",
					},
				},
			},
			want: false,
		},
		"DeleteFunc with node": {
			event: event.DeleteEvent{
				Object: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: node,
					},
				},
			},
			want: false,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var got bool
			switch e := test.event.(type) {
			case event.CreateEvent:
				got = predicate.Create(e)
			case event.UpdateEvent:
				got = predicate.Update(e)
			case event.DeleteEvent:
				got = predicate.Delete(e)
			default:
				t.Fatalf("unknown event type: %T", e)
			}

			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Errorf("predicate result mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestNewAllowlistJobFor(t *testing.T) {
	t.Setenv("MULTUS_IMAGE", "quay.io/openshift/multus:latest")

	expectedYAML := `
apiVersion: batch/v1
kind: Job
metadata:
  name: cni-sysctl-allowlist-test-node-12345
  namespace: openshift-multus
  labels:
    app: cni-sysctl-allowlist-job
    node: test-node
spec:
  backoffLimit: 3
  ttlSecondsAfterFinished: 86400
  activeDeadlineSeconds: 600
  template:
    metadata:
      labels:
        app: cni-sysctl-allowlist-job
        node: test-node
      annotations:
        target.workload.openshift.io/management: '{"effect": "PreferredDuringScheduling"}'
    spec:
      restartPolicy: Never
      priorityClassName: openshift-user-critical
      nodeSelector:
        kubernetes.io/hostname: test-node
      containers:
      - name: kube-multus-additional-cni-plugins
        image: quay.io/openshift/multus:latest
        command: ["/bin/bash", "-c", "cp /entrypoint/allowlist.conf /host/etc/cni/tuning/"]
        resources:
          requests:
            cpu: 10m
            memory: 10Mi
        securityContext:
          privileged: true
        terminationMessagePolicy: FallbackToLogsOnError
        volumeMounts:
        - name: cni-sysctl-allowlist
          mountPath: /entrypoint
        - name: tuning-conf-dir
          mountPath: /host/etc/cni/tuning/
          readOnly: false
      volumes:
      - name: cni-sysctl-allowlist
        configMap:
          name: cni-sysctl-allowlist
          defaultMode: 420
      - name: tuning-conf-dir
        hostPath:
          path: /etc/cni/tuning/
          type: DirectoryOrCreate
`

	var expectedJob batchv1.Job
	if err := yaml.Unmarshal([]byte(expectedYAML), &expectedJob); err != nil {
		t.Fatalf("failed to unmarshal expected YAML: %v", err)
	}

	actualJob := newAllowlistJobFor("test-node", testConfigMapVersion)

	opts := []cmp.Option{
		cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
	}

	if diff := cmp.Diff(&expectedJob, actualJob, opts...); diff != "" {
		t.Errorf("Job spec mismatch (-want +got):\n%s", diff)
	}
}
