package network

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/pkg/errors"

	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/render"
	iputil "github.com/openshift/cluster-network-operator/pkg/util/ip"
	k8sutil "github.com/openshift/cluster-network-operator/pkg/util/k8s"
)

const (
	OVNProvider = "ovn"
)

// renderKuryr returns manifests for Kuryr SDN.
// This includes manifests of
// - the openshift-kuryr namespace
// - kuryr RBAC resources
// - CRD's required by kuryr
// - configmap with kuryr.conf
// - the kuryr-controller deployment
// - the kuryr-daemon daemonset
func renderKuryr(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string) ([]*uns.Unstructured, error) {
	c := conf.DefaultNetwork.KuryrConfig
	b := bootstrapResult.Kuryr

	objs := []*uns.Unstructured{}

	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")

	// OpenStack cloud CA certificate provided by the user to the installer
	data.Data["UserCACertificate"] = b.UserCACert

	// general kuryr options
	data.Data["ResourceTags"] = "openshiftClusterID=" + b.ClusterID
	data.Data["PodSecurityGroups"] = strings.Join(b.PodSecurityGroups, ",")
	data.Data["WorkerNodesSubnet"] = b.WorkerNodesSubnet
	data.Data["WorkerNodesRouter"] = b.WorkerNodesRouter
	data.Data["PodSubnetpool"] = b.PodSubnetpool
	data.Data["ServiceSubnet"] = b.ServiceSubnet
	data.Data["ExternalNetwork"] = b.ExternalNetwork
	data.Data["OpenStackCloud"] = b.OpenStackCloud
	// FIXME(dulek): Move that logic to the template once it's known how to dereference pointers there.
	data.Data["OpenStackInsecureAPI"] = b.OpenStackCloud.Verify != nil && !*b.OpenStackCloud.Verify
	data.Data["DebugEnabled"] = conf.LogLevel != operv1.Normal

	// kuryr port pools options
	data.Data["EnablePortPoolsPrepopulation"] = c.EnablePortPoolsPrepopulation
	data.Data["PoolMaxPorts"] = c.PoolMaxPorts
	data.Data["PoolMinPorts"] = c.PoolMinPorts
	data.Data["PoolBatchPorts"] = c.PoolBatchPorts

	// deploy or not kuryr-admission-controller depending on double listeners support
	data.Data["AdmissionController"] = !b.OctaviaMultipleListeners

	// Octavia config data
	data.Data["OctaviaProvider"] = b.OctaviaProvider
	if b.OctaviaProvider == OVNProvider {
		data.Data["OctaviaMemberMode"] = "L2"
		data.Data["OctaviaSGMode"] = "create"
		data.Data["OctaviaSGEnforce"] = "false"
		data.Data["OctaviaLBAlgorithm"] = "SOURCE_IP_PORT"
	} else {
		data.Data["OctaviaMemberMode"] = "L3"
		data.Data["OctaviaSGMode"] = "update"
		data.Data["OctaviaSGEnforce"] = "true"
		data.Data["OctaviaLBAlgorithm"] = "ROUND_ROBIN"
	}
	data.Data["OctaviaVersion"] = b.OctaviaVersion

	// kuryr-daemon DaemonSet data
	data.Data["DaemonEnableProbes"] = true
	data.Data["DaemonProbesPort"] = c.DaemonProbesPort

	// kuryr-controller Deployment data
	data.Data["ControllerEnableProbes"] = true
	data.Data["ControllerProbesPort"] = c.ControllerProbesPort

	data.Data["CNIPluginsImage"] = os.Getenv("CNI_PLUGINS_IMAGE")
	data.Data["DaemonImage"] = os.Getenv("KURYR_DAEMON_IMAGE")
	data.Data["ControllerImage"] = os.Getenv("KURYR_CONTROLLER_IMAGE")
	data.Data["KUBERNETES_SERVICE_HOST"] = os.Getenv("KUBERNETES_SERVICE_HOST")
	data.Data["KUBERNETES_SERVICE_PORT"] = os.Getenv("KUBERNETES_SERVICE_PORT")
	data.Data["CNIConfDir"] = pluginCNIConfDir(conf)
	data.Data["CNIBinDir"] = CNIBinDir

	// We use MD5 hash of the JSONfied config data to make pods restart when
	// configuration was changed.
	hash, err := k8sutil.CalculateHash(data.Data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to calculate checksum of Kuryr configuration")
	}
	data.Data["ConfigMapHash"] = hash

	// DNS mutating webhook
	data.Data["AdmissionControllerSecret"] = names.KURYR_ADMISSION_CONTROLLER_SECRET
	data.Data["WebhookSecret"] = names.KURYR_WEBHOOK_SECRET
	data.Data["WebhookCA"] = b.WebhookCA
	data.Data["WebhookCAKey"] = b.WebhookCAKey
	data.Data["WebhookCert"] = b.WebhookCert
	data.Data["WebhookKey"] = b.WebhookKey

	// Nodes Network MTU
	data.Data["NodesNetworkMTU"] = b.NodesNetworkMTU

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/kuryr"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render manifests")
	}

	objs = append(objs, manifests...)
	return objs, nil
}

// validateKuryr checks that the Kuryr specific configuration is basically sane.
func validateKuryr(conf *operv1.NetworkSpec) []error {
	out := []error{}
	kc := conf.DefaultNetwork.KuryrConfig

	// NOTE(dulek): Dropping this constraint would require changes in Kuryr.
	if len(conf.ServiceNetwork) != 1 {
		out = append(out, errors.Errorf("serviceNetwork must have exactly 1 entry"))
	}

	// TODO(dulek): We should be able to drop this constraint once we test subnetpools with multiple CIDRs.
	if len(conf.ClusterNetwork) != 1 {
		out = append(out, errors.Errorf("clusterNetwork must have exactly 1 entry"))
	}

	_, svcNet, err := net.ParseCIDR(conf.ServiceNetwork[0])
	if err != nil {
		out = append(out, errors.Errorf("cannot parse serviceNetwork[0] CIDR"))
	}

	_, clusterNet, err := net.ParseCIDR(conf.ClusterNetwork[0].CIDR)
	if err != nil {
		out = append(out, errors.Errorf("cannot parse clusterNetwork[0].CIDR CIDR"))
	}

	var octaviaServiceNet *net.IPNet
	if kc != nil && kc.OpenStackServiceNetwork != "" {
		_, octaviaServiceNet, err = net.ParseCIDR(kc.OpenStackServiceNetwork)
		if err != nil {
			out = append(out, errors.Errorf("cannot parse defaultNetwork.kuryrConfig.octaviaServiceNetwork CIDR"))
		}
	} else {
		octaviaServiceNetObj := iputil.ExpandNet(*svcNet)
		octaviaServiceNet = &octaviaServiceNetObj
	}

	if kc != nil && kc.PoolBatchPorts != nil {
		if *kc.PoolBatchPorts > 0 {
			if kc.PoolMinPorts > 0 && *kc.PoolBatchPorts < kc.PoolMinPorts {
				out = append(out, errors.Errorf("poolBatchPorts cannot be set below poolMinPorts"))
			}
			if kc.PoolMaxPorts > 0 && *kc.PoolBatchPorts > kc.PoolMaxPorts {
				out = append(out, errors.Errorf("poolBatchPorts cannot be set above poolMaxPorts"))
			}
		} else {
			out = append(out, errors.Errorf("poolBatchPorts has to have at least value of 1"))
		}
	}

	if octaviaServiceNet != nil {
		if clusterNet != nil && iputil.NetsOverlap(*octaviaServiceNet, *clusterNet) {
			out = append(out, errors.Errorf("octaviaServiceNetwork %s will overlap with cluster network %s "+
				octaviaServiceNet.String(), conf.ClusterNetwork[0].CIDR))
		}

		if svcNet != nil {
			if !iputil.NetIncludes(*octaviaServiceNet, *svcNet) {
				out = append(out, errors.Errorf("octaviaServiceNetwork %s does not include serviceNetwork %s "+
					"(the octaviaServiceNetwork needs to be twice the size of serviceNetwork and include it)",
					octaviaServiceNet.String(), svcNet.String()))
			}

			octaviaNetPrefixLen, _ := octaviaServiceNet.Mask.Size()
			svcNetPrefixLen, _ := svcNet.Mask.Size()
			if octaviaNetPrefixLen >= svcNetPrefixLen {
				out = append(out, errors.Errorf("octaviaServiceNetwork %s is too small comparing to serviceNetwork %s "+
					"(the octaviaServiceNetwork needs to be twice the size of the serviceNetwork and include it)",
					octaviaServiceNet.String(), svcNet.String()))
			}
		}
	}

	return out
}

// isKuryrChangeSafe makes sure to only allow changes applied to kuryr.conf
// and not to the resources created in the bootstrap process.
func isKuryrChangeSafe(prev, next *operv1.NetworkSpec) []error {
	pn := prev.DefaultNetwork.KuryrConfig
	nn := next.DefaultNetwork.KuryrConfig
	errs := []error{}

	if reflect.DeepEqual(pn, nn) {
		return errs
	}

	if pn.OpenStackServiceNetwork != nn.OpenStackServiceNetwork {
		errs = append(errs, errors.Errorf("cannot change kuryr openStackServiceNetwork"))
	}

	return errs
}

func fillKuryrDefaults(conf *operv1.NetworkSpec) {
	if conf.DefaultNetwork.KuryrConfig == nil {
		// We don't have anything required in KuryrConfig yet, so we can just create it if needed.
		conf.DefaultNetwork.KuryrConfig = &operv1.KuryrConfig{}
	}
	kc := conf.DefaultNetwork.KuryrConfig

	if kc.DaemonProbesPort == nil {
		var port uint32 = 8090
		kc.DaemonProbesPort = &port
	}

	if kc.ControllerProbesPort == nil {
		var port uint32 = 8091
		kc.ControllerProbesPort = &port
	}

	if kc.OpenStackServiceNetwork == "" {
		_, svcNet, _ := net.ParseCIDR(conf.ServiceNetwork[0])
		octaviaServiceNet := iputil.ExpandNet(*svcNet)
		kc.OpenStackServiceNetwork = octaviaServiceNet.String()
	}

	if kc.PoolMinPorts == 0 {
		kc.PoolMinPorts = 1
	}

	if kc.PoolBatchPorts == nil {
		var batchPorts uint = 3
		kc.PoolBatchPorts = &batchPorts
	}
}
