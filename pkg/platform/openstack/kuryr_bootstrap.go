package openstack

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	b64 "encoding/base64"
	"fmt"
	"k8s.io/api/core/v1"
	"log"
	"net"
	"net/http"
	"regexp"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/rules"
	"github.com/gophercloud/utils/openstack/clientconfig"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/platform/openstack/util/cert"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	confv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"

	iputil "github.com/openshift/cluster-network-operator/pkg/util/ip"
)

const (
	CloudsSecret             = "installer-cloud-credentials"
	CloudName                = "openstack"
	CloudsSecretKey          = "clouds.yaml"
	OpenShiftConfigNamespace = "openshift-config"
	UserCABundleConfigMap    = "cloud-provider-config"
	// NOTE(dulek): This one is hardcoded in openshift/installer.
	InfrastructureCRDName                  = "cluster"
	MinOctaviaVersionWithMultipleListeners = "v2.11"
	MinOctaviaVersionWithHTTPSMonitors     = "v2.10"
	MinOctaviaVersionWithProviders         = "v2.6"
	MinOctaviaVersionWithTagSupport        = "v2.5"
	MinOctaviaVersionWithTimeouts          = "v2.1"
	KuryrNamespace                         = "openshift-kuryr"
	KuryrConfigMapName                     = "kuryr-config"
	// NOTE(ltomasbo): Only OVN octavia driver supported on kuryr
	OVNProvider = "ovn"
	EtcdPort    = 2379
	DNSPort     = 53
)

func GetClusterID(kubeClient client.Client) (string, error) {
	cluster := &confv1.Infrastructure{
		TypeMeta:   metav1.TypeMeta{APIVersion: "config.openshift.io/v1", Kind: "Infrastructure"},
		ObjectMeta: metav1.ObjectMeta{Name: InfrastructureCRDName},
	}

	err := kubeClient.Get(context.TODO(), client.ObjectKey{Name: InfrastructureCRDName}, cluster)
	if err != nil {
		return "", errors.Wrapf(err, "Failed to get Infrastracture CRD %s", InfrastructureCRDName)
	}
	return cluster.Status.InfrastructureName, nil
}

func GetCloudFromSecret(kubeClient client.Client) (clientconfig.Cloud, error) {
	emptyCloud := clientconfig.Cloud{}

	secret := &v1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: CloudsSecret},
	}

	err := kubeClient.Get(context.TODO(), client.ObjectKey{Namespace: names.APPLIED_NAMESPACE, Name: CloudsSecret}, secret)
	if err != nil {
		return emptyCloud, errors.Wrapf(err, "Failed to get %s Secret with OpenStack credentials", CloudsSecret)
	}

	content, ok := secret.Data[CloudsSecretKey]
	if !ok {
		return emptyCloud, errors.Errorf("OpenStack credentials secret %v did not contain key %v",
			CloudsSecret, CloudsSecretKey)
	}
	var clouds clientconfig.Clouds
	err = yaml.Unmarshal(content, &clouds)
	if err != nil {
		return emptyCloud, errors.Wrapf(err, "failed to unmarshal clouds credentials stored in secret %v", CloudsSecret)
	}

	return clouds.Clouds[CloudName], nil
}

func generateName(name, clusterID string) string {
	return fmt.Sprintf("%s-%s", clusterID, name)
}

func ensureCA(kubeClient client.Client) ([]byte, []byte, error) {
	secret := &v1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: names.KURYR_ADMISSION_CONTROLLER_SECRET},
	}
	err := kubeClient.Get(context.TODO(), client.ObjectKey{Namespace: KuryrNamespace, Name: names.KURYR_ADMISSION_CONTROLLER_SECRET}, secret)
	if err != nil {
		caBytes, keyBytes, err := cert.GenerateCA("Kuryr")
		if err != nil {
			return nil, nil, errors.Wrapf(err, "Failed to generate CA.")
		}
		return caBytes, keyBytes, nil
	} else {
		crtContent, crtok := secret.Data["ca.crt"]
		keyContent, keyok := secret.Data["ca.key"]
		if !crtok || !keyok {
			caBytes, keyBytes, err := cert.GenerateCA("Kuryr")
			if err != nil {
				return nil, nil, errors.Wrapf(err, "Failed to generate CA.")
			}
			return caBytes, keyBytes, nil
		}
		return crtContent, keyContent, nil
	}
}

func ensureCertificate(kubeClient client.Client, caPEM []byte, privateKey []byte) ([]byte, []byte, error) {
	secret := &v1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: names.KURYR_WEBHOOK_SECRET},
	}
	err := kubeClient.Get(context.TODO(), client.ObjectKey{Namespace: KuryrNamespace, Name: names.KURYR_WEBHOOK_SECRET}, secret)
	if err != nil {
		caBytes, keyBytes, err := cert.GenerateCertificate("Kuryr", []string{"kuryr-dns-admission-controller.openshift-kuryr", "kuryr-dns-admission-controller.openshift-kuryr.svc"}, caPEM, privateKey)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "Failed to generate CA.")
		}
		return caBytes, keyBytes, nil
	} else {
		crtContent, crtok := secret.Data["tls.crt"]
		keyContent, keyok := secret.Data["tls.key"]
		if !crtok || !keyok {
			caBytes, keyBytes, err := cert.GenerateCertificate("Kuryr", []string{"kuryr-dns-admission-controller.openshift-kuryr", "kuryr-dns-admission-controller.openshift-kuryr.svc"}, caPEM, privateKey)
			if err != nil {
				return nil, nil, errors.Wrapf(err, "Failed to generate CA.")
			}
			return caBytes, keyBytes, nil
		}
		return crtContent, keyContent, nil
	}
}

func getConfigMap(kubeClient client.Client, namespace, name string) (*v1.ConfigMap, error) {
	cm := &v1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	err := kubeClient.Get(context.TODO(), client.ObjectKey{Namespace: namespace, Name: name}, cm)
	if err != nil {
		return nil, err
	}
	return cm, nil
}

func getCloudProviderCACert(kubeClient client.Client) (string, error) {
	cm, err := getConfigMap(kubeClient, OpenShiftConfigNamespace, UserCABundleConfigMap)
	if err != nil {
		return "", err
	}
	return cm.Data["ca-bundle.pem"], nil
}

func getSavedOctaviaProvider(kubeClient client.Client) (string, error) {
	cm, err := getConfigMap(kubeClient, KuryrNamespace, KuryrConfigMapName)
	if err != nil {
		return "", err
	}
	return cm.Annotations[names.KuryrOctaviaProviderAnnotation], nil
}

// Logs into OpenStack and creates all the resources that are required to run
// Kuryr based on conf NetworkConfigSpec. Basically this includes service
// network and subnet, pods subnetpool, security group and load balancer for
// OpenShift API. Besides that it looks up router and subnet used by OpenShift
// worker nodes (needed to configure Kuryr) and makes sure there's a routing
// between them and created subnets. Also SG rules are added to make sure pod
// subnet can reach nodes and nodes can reach pods and services. The data is
// returned to populate Kuryr's configuration.
func BootstrapKuryr(conf *operv1.NetworkSpec, kubeClient client.Client) (*bootstrap.BootstrapResult, error) {
	log.Print("Kuryr bootstrap started")
	kc := conf.DefaultNetwork.KuryrConfig

	clusterID, err := GetClusterID(kubeClient)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get ClusterID")
	}

	cloud, err := GetCloudFromSecret(kubeClient)
	if err != nil {
		return nil, errors.Wrap(err, "failed to authenticate to OpenStack")
	}

	clientOpts := new(clientconfig.ClientOpts)

	if cloud.AuthInfo != nil {
		clientOpts.AuthInfo = cloud.AuthInfo
		clientOpts.AuthType = cloud.AuthType
		clientOpts.Cloud = cloud.Cloud
		clientOpts.RegionName = cloud.RegionName
	}

	opts, err := clientconfig.AuthOptions(clientOpts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to authenticate to OpenStack")
	}
	provider, err := openstack.NewClient(opts.IdentityEndpoint)
	if err != nil {
		return nil, errors.Wrap(err, "failed to authenticate to OpenStack")
	}

	// We need to fetch user-provided OpenStack cloud CA certificate and make gophercloud use it.
	// Also it'll get injected into a ConfigMap mounted into kuryr-controller later on.
	userCACert, err := getCloudProviderCACert(kubeClient)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, errors.Wrap(err, "failed to get cloud provider CA certificate")
	}

	if userCACert != "" {
		certPool, err := x509.SystemCertPool()
		if err == nil {
			certPool.AppendCertsFromPEM([]byte(userCACert))
			client := http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						RootCAs: certPool,
					},
				},
			}
			provider.HTTPClient = client
		}
	}

	err = openstack.Authenticate(provider, *opts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to authenticate to OpenStack")
	}

	// Kuryr will need ProjectID to be set, let's make sure it's set.
	if cloud.AuthInfo.ProjectID == "" && cloud.AuthInfo.ProjectName != "" {
		keystone, err := openstack.NewIdentityV3(provider, gophercloud.EndpointOpts{})
		if err != nil {
			return nil, errors.Wrap(err, "failed to create Keystone client")
		}

		projectID, err := getProjectID(keystone)
		if err != nil {
			return nil, errors.Wrap(err, "failed to find project ID")
		}
		cloud.AuthInfo.ProjectID = projectID
	}

	client, err := openstack.NewNetworkV2(provider, gophercloud.EndpointOpts{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to create Neutron client")
	}

	tag := "openshiftClusterID=" + clusterID
	log.Printf("Using %s as resources tag", tag)

	log.Print("Ensuring services network")
	svcNetId, err := ensureOpenStackNetwork(client, generateName("kuryr-service-network", clusterID), tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create service network")
	}
	log.Printf("Services network %s present", svcNetId)

	// Service subnet
	// We need last usable IP from this CIDR. We use first subnet, we don't support multiple entries in Kuryr.
	_, svcNet, err := net.ParseCIDR(conf.ServiceNetwork[0])
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to parse ServiceNetwork CIDR %s", conf.ServiceNetwork[0])
	}

	openStackSvcCIDR := kc.OpenStackServiceNetwork
	_, openStackSvcNet, _ := net.ParseCIDR(openStackSvcCIDR)
	allocationRanges := iputil.UsableNonOverlappingRanges(*openStackSvcNet, *svcNet)
	// OpenShift will use svcNet range. In allocationRanges we have parts of openStackSvcNet that are not overlapping
	// with svcNet. We will put gatewayIP on the highest usable IP from those ranges. We need to exclude that IP from
	// the ranges we pass to Neutron or it will complain.
	gatewayIP := allocationRanges[len(allocationRanges)-1].End
	allocationRanges[len(allocationRanges)-1].End = iputil.IterateIP4(gatewayIP, -1)

	log.Printf("Ensuring services subnet with %s CIDR (services from %s) and %s gateway with allocation pools %+v",
		openStackSvcCIDR, conf.ServiceNetwork[0], gatewayIP.String(), allocationRanges)
	svcSubnetId, err := ensureOpenStackSubnet(client, generateName("kuryr-service-subnet", clusterID), tag,
		svcNetId, openStackSvcCIDR, gatewayIP.String(), allocationRanges)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create service subnet")
	}
	log.Printf("Services subnet %s present", svcSubnetId)

	// Pod subnetpool
	podSubnetCidrs := make([]string, len(conf.ClusterNetwork))
	for i, cn := range conf.ClusterNetwork {
		podSubnetCidrs[i] = cn.CIDR
	}
	// TODO(dulek): Now we only support one ClusterNetwork, so we take first HostPrefix. If we're to support multiple,
	//              we need to validate if all of them are the same - that's how it can work in OpenStack.
	prefixLen := conf.ClusterNetwork[0].HostPrefix
	log.Printf("Ensuring pod subnetpool with following CIDRs: %v", podSubnetCidrs)
	podSubnetpoolId, err := ensureOpenStackSubnetpool(client, generateName("kuryr-pod-subnetpool", clusterID), tag,
		podSubnetCidrs, prefixLen)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create pod subnetpool")
	}
	log.Printf("Pod subnetpool %s present", podSubnetpoolId)

	workerSubnet, err := findOpenStackSubnet(client, generateName("nodes", clusterID), tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find worker nodes subnet")
	}
	log.Printf("Found worker nodes subnet %s", workerSubnet.ID)
	router, err := findOpenStackRouter(client, generateName("external-router", clusterID), tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find worker nodes router")
	}
	routerId := router.ID
	externalNetwork := router.GatewayInfo.NetworkID
	log.Printf("Found worker nodes router %s", routerId)
	ps, err := getOpenStackRouterPorts(client, routerId)
	if err != nil {
		return nil, errors.Wrap(err, "failed list ports of worker nodes router")
	}

	if !lookupOpenStackPort(ps, svcSubnetId) {
		log.Printf("Ensuring service subnet router port with %s IP", gatewayIP.String())
		portId, err := ensureOpenStackPort(client, generateName("kuryr-service-subnet-router-port", clusterID), tag,
			svcNetId, svcSubnetId, gatewayIP.String())
		if err != nil {
			return nil, errors.Wrap(err, "failed to create service subnet router port")
		}
		log.Printf("Service subnet router port %s present, adding it as interface.", portId)
		err = ensureOpenStackRouterInterface(client, routerId, nil, &portId)
		if err != nil {
			return nil, errors.Wrap(err, "failed to create service subnet router interface")
		}
	}

	masterSgId, err := findOpenStackSgId(client, generateName("master", clusterID), tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find master nodes security group")
	}
	log.Printf("Found master nodes security group %s", masterSgId)
	workerSgId, err := findOpenStackSgId(client, generateName("worker", clusterID), tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find worker nodes security group")
	}
	log.Printf("Found worker nodes security group %s", workerSgId)

	log.Print("Ensuring pods security group")
	podSgId, err := ensureOpenStackSg(client, generateName("kuryr-pods-security-group", clusterID), tag)
	log.Printf("Pods security group %s present", podSgId)

	log.Print("Allowing traffic from pod to pod")
	err = ensureOpenStackSgRule(client, podSgId, "0.0.0.0/0", -1, -1, rules.ProtocolTCP)
	if err != nil {
		return nil, errors.Wrap(err, "failed to add rule opening traffic pod to pod")
	}
	for _, cidr := range podSubnetCidrs {
		err = ensureOpenStackSgRule(client, masterSgId, cidr, EtcdPort, EtcdPort, rules.ProtocolTCP)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to add TCP rule opening traffic to masters from %s on %d", cidr, EtcdPort)
		}
		err = ensureOpenStackSgRule(client, masterSgId, cidr, DNSPort, DNSPort, rules.ProtocolTCP)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to add TCP rule opening traffic to masters from %s on %d", cidr, DNSPort)
		}
		err = ensureOpenStackSgRule(client, masterSgId, cidr, DNSPort, DNSPort, rules.ProtocolUDP)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to add UDP rule opening traffic to masters from %s on %d", cidr, DNSPort)
		}
		err = ensureOpenStackSgRule(client, workerSgId, cidr, DNSPort, DNSPort, rules.ProtocolTCP)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to add TCP rule opening traffic to workers from %s on %d", cidr, DNSPort)
		}
		err = ensureOpenStackSgRule(client, workerSgId, cidr, DNSPort, DNSPort, rules.ProtocolUDP)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to add UDP rule opening traffic to workers from %s on %d", cidr, DNSPort)
		}
	}
	err = ensureOpenStackSgRule(client, masterSgId, openStackSvcCIDR, EtcdPort, EtcdPort, rules.ProtocolTCP)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to add rule opening etcd traffic to masters from service subnet %s", conf.ServiceNetwork[0])
	}
	// We need to open traffic from service subnet to masters for API LB to work.
	err = ensureOpenStackSgRule(client, masterSgId, openStackSvcCIDR, 6443, 6443, rules.ProtocolTCP)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to add rule opening traffic to masters from service subnet %s", conf.ServiceNetwork[0])
	}
	log.Print("All requried traffic allowed")

	lbClient, err := openstack.NewLoadBalancerV2(provider, gophercloud.EndpointOpts{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to create Octavia client")
	}

	// We need first usable IP from services CIDR
	// This will get us the first one (subnet IP)
	apiIP := iputil.FirstUsableIP(*svcNet)
	log.Printf("Creating OpenShift API loadbalancer with IP %s", apiIP.String())
	lbId, err := ensureOpenStackLb(lbClient, generateName("kuryr-api-loadbalancer", clusterID), apiIP.String(), svcSubnetId, tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create OpenShift API loadbalancer")
	}
	log.Printf("OpenShift API loadbalancer %s present", lbId)

	log.Print("Creating OpenShift API loadbalancer pool")
	poolId, err := ensureOpenStackLbPool(lbClient, generateName("kuryr-api-loadbalancer-pool", clusterID), lbId)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create OpenShift API loadbalancer pool")
	}
	log.Printf("OpenShift API loadbalancer pool %s present", poolId)

	log.Print("Creating OpenShift API loadbalancer health monitor")
	monitorId, err := ensureOpenStackLbMonitor(lbClient, "kuryr-api-loadbalancer-monitor", poolId)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create OpenShift API loadbalancer health monitor")
	}
	log.Printf("OpenShift API loadbalancer health monitor %s present", monitorId)

	log.Print("Creating OpenShift API loadbalancer listener")
	listenerId, err := ensureOpenStackLbListener(lbClient, generateName("kuryr-api-loadbalancer-listener", clusterID),
		lbId, poolId, 443)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create OpenShift API loadbalancer listener")
	}
	log.Printf("OpenShift API loadbalancer listener %s present", listenerId)

	// We need to list all master ports and add them to the LB pool. We also add the
	// bootstrap node port as for a portion of installation only it provides access to
	// the API. With healthchecks enabled for the pool we'll get masters added automatically
	// when they're up and ready.
	log.Print("Creating OpenShift API loadbalancer pool members")
	r, _ := regexp.Compile(fmt.Sprintf("^%s-(master-port-[0-9]+|bootstrap-port)$", clusterID))
	portList, err := listOpenStackPortsMatchingPattern(client, tag, r)
	for _, port := range portList {
		if len(port.FixedIPs) > 0 {
			portIp := port.FixedIPs[0].IPAddress
			log.Printf("Found port %s with IP %s", port.ID, portIp)

			// We want bootstrap to stop being used as soon as possible, as it will serve
			// outdated data during the bootstrap transition period.
			weight := 100
			if strings.HasSuffix(port.Name, "bootstrap-port") {
				weight = 1
			}

			memberId, err := ensureOpenStackLbPoolMember(lbClient, port.Name, lbId,
				poolId, portIp, svcSubnetId, 6443, weight)
			if err != nil {
				log.Printf("Failed to add port %s (%s) to LB pool %s: %s", port.ID, port.Name, poolId, err)
				continue
			}
			log.Printf("Added member %s to LB pool %s", memberId, poolId)
		} else {
			log.Printf("Matching port %s has no IP", port.ID)
		}
	}

	log.Print("Ensuring certificates")
	ca, key, err := ensureCA(kubeClient)
	if err != nil {
		return nil, errors.Wrap(err, "failed to ensure CA")
	}
	webhookCert, webhookKey, err := ensureCertificate(kubeClient, ca, key)
	if err != nil {
		return nil, errors.Wrap(err, "failed to ensure Certificate")
	}

	log.Print("Checking OVN Octavia driver support")
	octaviaProviderSupport, err := IsOctaviaVersionSupported(lbClient, MinOctaviaVersionWithProviders)
	if err != nil {
		return nil, errors.Wrap(err, "failed to determine if Octavia supports providers")
	}

	log.Print("Checking Double Listeners Octavia support")
	octaviaMultipleListenersSupport, err := IsOctaviaVersionSupported(lbClient, MinOctaviaVersionWithMultipleListeners)
	if err != nil {
		return nil, errors.Wrap(err, "failed to determine if Octavia supports double listeners")
	}

	// Logic here is as follows:
	// 1. We don't want to suddenly reconfigure Kuryr to use different provider, so we always try to fetch the currently
	//    used one from annotation added to kuryr-config ConfigMap first.
	// 2. If fetching annotation fails, then either it's the first run and we're yet to create the ConfigMap or user
	//    deleted the annotation in order for us to trigger the reconfiguration. In both cases proceed with detection:
	//    a. Check if Octavia version supports provider discovery.
	//    b. List providers and look for OVN one.
	//    c. If it's present configure Kuryr to use it.
	//    d. In case of any issues just use whatever the default is.
	octaviaProvider, err := getSavedOctaviaProvider(kubeClient)
	if err != nil && !apierrors.IsNotFound(err) { // Ignore 404, just do the normal discovery then.
		return nil, errors.Wrap(err, "failed to get kuryr-config ConfigMap")
	}
	if octaviaProvider != "" {
		log.Printf("Detected that Kuryr was already configured to use %s LB provider. Making sure to keep it that way.",
			octaviaProvider)
		if octaviaProvider == OVNProvider {
			// OVN provider does not support multiple listeners, so in that case we always set false to that.
			octaviaMultipleListenersSupport = false
		}
	} else {
		octaviaProvider = "default"
		if octaviaProviderSupport {
			providerList, err := listOpenStackOctaviaProviders(lbClient)
			if err != nil {
				log.Print("failed to get lbs provider list, using default octavia provider")
			} else {
				for _, provider := range providerList {
					if provider.Name == OVNProvider {
						log.Print("OVN Provider is enabled and Kuryr will use it")
						octaviaProvider = OVNProvider
						// OVN provider does not support multiple listeners, so in that case we always set false to that.
						octaviaMultipleListenersSupport = false
					}
				}
			}
		}
	}

	log.Print("Kuryr bootstrap finished")

	res := bootstrap.BootstrapResult{
		Kuryr: bootstrap.KuryrBootstrapResult{
			ServiceSubnet:            svcSubnetId,
			PodSubnetpool:            podSubnetpoolId,
			WorkerNodesRouter:        routerId,
			WorkerNodesSubnet:        workerSubnet.ID,
			PodSecurityGroups:        []string{podSgId},
			ExternalNetwork:          externalNetwork,
			ClusterID:                clusterID,
			OctaviaProvider:          octaviaProvider,
			OctaviaMultipleListeners: octaviaMultipleListenersSupport,
			OpenStackCloud:           cloud,
			WebhookCA:                b64.StdEncoding.EncodeToString(ca),
			WebhookCAKey:             b64.StdEncoding.EncodeToString(key),
			WebhookKey:               b64.StdEncoding.EncodeToString(webhookKey),
			WebhookCert:              b64.StdEncoding.EncodeToString(webhookCert),
			UserCACert:               userCACert,
		}}
	return &res, nil
}
