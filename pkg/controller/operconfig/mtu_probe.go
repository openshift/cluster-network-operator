package operconfig

import (
	"context"
	"fmt"
	"os"
	"time"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/openshift/cluster-network-operator/pkg/util"

	configv1 "github.com/openshift/api/config/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	awsMTU        = 9001
	azureMTU      = 1500
	defaultHCPMTU = 1500 // Safe default for HyperShift platforms without a known uplink MTU
)

// probeMTU returns the host (uplink) MTU for the cluster.
//
// In HyperShift, the MTU prober job cannot run because CNO does not have access
// to the hosted cluster's worker nodes. Instead, platform-specific defaults are
// returned. If the user has explicitly set the tunnel MTU on the Network CR (via
// HyperShift's --ovn-kubernetes-mtu flag), NeedMTUProbe returns false and this
// function is never called — the user value is preserved as-is.
//
// For standalone clusters, a prober job is deployed to detect the host MTU
// from a worker node's default route interface.
func (r *ReconcileOperConfig) probeMTU(ctx context.Context, oc *operv1.Network, infra *bootstrap.InfraStatus) (int, error) {
	// In HyperShift the MTU prober job cannot access guest worker nodes,
	// so we return platform-specific defaults for the host (uplink) MTU.
	// These defaults are only used when the user has not explicitly set
	// the tunnel MTU on the Network CR; in that case NeedMTUProbe returns
	// false and probeMTU is never called.
	if infra.HostedControlPlane != nil {
		switch infra.PlatformType {
		case configv1.AWSPlatformType:
			klog.Infof("HyperShift AWS cluster, omitting MTU probing and using default of %d", awsMTU)
			return awsMTU, nil
		case configv1.AzurePlatformType:
			klog.Infof("HyperShift Azure cluster, omitting MTU probing and using default of %d", azureMTU)
			return azureMTU, nil
		default:
			// For platforms without a known uplink MTU (OpenStack, Agent, PowerVS, etc.),
			// use 1500 (standard Ethernet MTU) as a safe default. Users can override
			// this by setting --ovn-kubernetes-mtu on the HostedCluster.
			klog.Infof("HyperShift %s cluster, omitting MTU probing and using safe default of %d", infra.PlatformType, defaultHCPMTU)
			return defaultHCPMTU, nil
		}
	}
	mtu, err := util.ReadMTUConfigMap(ctx, r.client)
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
	r.mtuProberCleanedUp = false
	defer func() {
		if err := r.deleteMTUProber(ctx, infra); err != nil {
			klog.Errorf("Failed to clean up mtu prober: %v", err)
		}
	}()

	klog.Info("MTU prober deployed, waiting for result ConfigMap")

	// wait up to 100 seconds for Job to report back.
	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 100*time.Second, false, func(ctx context.Context) (bool, error) {
		var err error
		mtu, err = util.ReadMTUConfigMap(ctx, r.client)
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
		return mtu, nil
	}

	return 0, fmt.Errorf("timed out getting result from MTU prober %v", err)
}

func (r *ReconcileOperConfig) deployMTUProber(ctx context.Context, owner metav1.Object, infra *bootstrap.InfraStatus) error {
	objs, err := renderMTUProber(infra)
	if err != nil {
		return err
	}

	klog.Info("No probed MTU detected, deploying mtu-prober job")
	for _, obj := range objs {
		if err := controllerutil.SetControllerReference(owner, obj, r.client.ClientFor(apply.GetClusterName(obj)).Scheme()); err != nil {
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

	objs, err := renderMTUProber(infra)
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

func renderMTUProber(infra *bootstrap.InfraStatus) ([]*uns.Unstructured, error) {
	data := render.MakeRenderData()
	data.Data["CNOImage"] = os.Getenv("NETWORK_CHECK_TARGET_IMAGE")
	data.Data["KUBERNETES_SERVICE_HOST"] = infra.APIServers[bootstrap.APIServerDefault].Host
	data.Data["KUBERNETES_SERVICE_PORT"] = infra.APIServers[bootstrap.APIServerDefault].Port
	data.Data["DestNS"] = util.MTU_CM_NAMESPACE
	data.Data["DestName"] = util.MTU_CM_NAME
	data.Data["HTTP_PROXY"] = ""
	data.Data["HTTPS_PROXY"] = ""
	data.Data["NO_PROXY"] = ""
	if infra.ControlPlaneTopology == configv1.ExternalTopologyMode {
		data.Data["HTTP_PROXY"] = infra.Proxy.HTTPProxy
		data.Data["HTTPS_PROXY"] = infra.Proxy.HTTPSProxy
		data.Data["NO_PROXY"] = infra.Proxy.NoProxy
	}

	objs, err := render.RenderDir("bindata/network/mtu-prober", &data)
	if err != nil {
		return nil, err
	}
	return objs, nil
}
