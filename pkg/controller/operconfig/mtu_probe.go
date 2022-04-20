package operconfig

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	operv1 "github.com/openshift/api/operator/v1"

	"github.com/openshift/cluster-network-operator/pkg/apply"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/render"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	cmNamespace = "openshift-network-operator"
	cmName      = "mtu"
)

// probeMTU executes the MTU prober job, if the result configmap
// doesn't exist. It then waits 100 seconds for results to be written,
// then cleans up after itsef.
// If, for whatever reason, it takes longer for the MTU to be detected,
// it will adopt an existing job.
func (r *ReconcileOperConfig) probeMTU(ctx context.Context, oc *operv1.Network, infra *bootstrap.InfraStatus) (int, error) {
	mtu, err := r.readMTUConfigMap(ctx)
	if err == nil {
		_ = r.deleteMTUProber(ctx, infra)
		return mtu, nil
	} else if !apierrors.IsNotFound(err) {
		return 0, err
	}

	// cm doesn't exist, create Job
	err = r.deployMTUProber(ctx, oc, infra)
	if err != nil {
		return 0, fmt.Errorf("failed to deploy mtu prober: %w", err)
	}

	klog.Info("MTU prober deployed, waiting for result ConfigMap")

	// wait up to 100 seconds for Job to report back.
	err = wait.PollWithContext(ctx, 5*time.Second, 100*time.Second, func(ctx context.Context) (bool, error) {
		var err error
		mtu, err = r.readMTUConfigMap(ctx)
		if err == nil {
			return true, nil
		} else if apierrors.IsNotFound(err) {
			return false, nil
		}
		// log and swallow the error - we always want to retry
		// otherwise Poll will short-circuit
		klog.Errorf("Failed to retrieve the MTU result ConfigMap (may retry): %v", err)
		return false, nil
	})

	if err == nil {
		if err := r.deleteMTUProber(ctx, infra); err != nil {
			klog.Errorf("failed to clean up mtu prober: %v", err)
		}
		return mtu, nil
	}

	return 0, fmt.Errorf("timed out getting result from MTU prober %v", err)
}

func (r *ReconcileOperConfig) readMTUConfigMap(ctx context.Context) (int, error) {
	klog.V(4).Infof("Looking for ConfigMap %s/%s", cmNamespace, cmName)
	cm := &corev1.ConfigMap{}
	err := r.client.Default().CRClient().Get(ctx, types.NamespacedName{Namespace: cmNamespace, Name: cmName}, cm)
	if err != nil {
		return 0, err
	}
	mtu, err := strconv.Atoi(cm.Data["mtu"])
	if err != nil || mtu == 0 {
		return 0, fmt.Errorf("format error")
	}

	klog.V(2).Infof("Found mtu %d", mtu)
	return mtu, nil
}

func (r *ReconcileOperConfig) deployMTUProber(ctx context.Context, owner metav1.Object, infra *bootstrap.InfraStatus) error {
	var proxy configv1.Proxy
	if err := r.client.Default().CRClient().Get(ctx, crclient.ObjectKey{Name: "cluster"}, &proxy); err != nil {
		return fmt.Errorf("failed to get proxy: %w", err)
	}
	objs, err := renderMTUProber(infra, proxy.Status)
	if err != nil {
		return err
	}

	klog.Info("No probed MTU detected, deploying mtu-prober job")
	for _, obj := range objs {
		if err := controllerutil.SetControllerReference(owner, obj, r.scheme); err != nil {
			return err // unlikely
		}
		if err := apply.ApplyObject(ctx, r.client, obj, ControllerName); err != nil {
			klog.Infof("Could not apply mtu-prober object: %v", err)
			return err
		}
	}
	return nil
}

func (r *ReconcileOperConfig) deleteMTUProber(ctx context.Context, infra *bootstrap.InfraStatus) error {
	if r.mtuProberCleanedUp {
		return nil
	}

	var proxy configv1.Proxy
	if err := r.client.Default().CRClient().Get(ctx, crclient.ObjectKey{Name: "cluster"}, &proxy); err != nil {
		return fmt.Errorf("failed to get proxy: %w", err)
	}
	objs, err := renderMTUProber(infra, proxy.Status)
	if err != nil {
		return err
	}

	klog.Info("Cleaning up mtu-prober job")
	for i := len(objs) - 1; i >= 0; i-- {
		obj := objs[i]
		if err := r.client.Default().CRClient().Delete(ctx, obj, crclient.PropagationPolicy("Background")); err != nil && !apierrors.IsNotFound(err) {
			klog.Infof("Could not delete mtu-prober object: %v", err)
		} else {
			klog.Infof("Deleted %s %s/%s", obj.GetKind(), obj.GetNamespace(), obj.GetName())
		}
	}
	r.mtuProberCleanedUp = true
	return nil
}

func renderMTUProber(infra *bootstrap.InfraStatus, proxyStatus configv1.ProxyStatus) ([]*uns.Unstructured, error) {
	data := render.MakeRenderData()
	data.Data["CNOImage"] = os.Getenv("NETWORK_CHECK_TARGET_IMAGE")
	data.Data["KUBERNETES_SERVICE_HOST"] = infra.APIServers[bootstrap.APIServerDefault].Host
	data.Data["KUBERNETES_SERVICE_PORT"] = infra.APIServers[bootstrap.APIServerDefault].Port
	data.Data["DestNS"] = cmNamespace
	data.Data["DestName"] = cmName
	data.Data["HTTP_PROXY"] = proxyStatus.HTTPProxy
	data.Data["HTTPS_PROXY"] = proxyStatus.HTTPSProxy
	data.Data["NO_PROXY"] = proxyStatus.NoProxy

	objs, err := render.RenderDir("bindata/network/mtu-prober", &data)
	if err != nil {
		return nil, err
	}
	return objs, nil
}
