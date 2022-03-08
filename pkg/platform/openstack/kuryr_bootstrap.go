package openstack

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	b64 "encoding/base64"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"

	"golang.org/x/net/http/httpproxy"

	v1 "k8s.io/api/core/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Masterminds/semver"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/rules"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/subnets"
	"github.com/gophercloud/utils/openstack/clientconfig"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/platform"
	"github.com/openshift/cluster-network-operator/pkg/platform/openstack/util/cert"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"

	confv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	machineapi "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	capo "sigs.k8s.io/cluster-api-provider-openstack/pkg/apis/openstackproviderconfig/v1alpha1"

	iputil "github.com/openshift/cluster-network-operator/pkg/util/ip"
)

const (
	CloudsSecret             = "installer-cloud-credentials"
	CloudName                = "openstack"
	CloudsSecretKey          = "clouds.yaml"
	OpenShiftConfigNamespace = "openshift-config"
	UserCABundleConfigMap    = "cloud-provider-config"
	// NOTE(dulek): This one is hardcoded in openshift/installer.
	InfrastructureCRDName = "cluster"
	masterMachineLabel    = "machine.openshift.io/cluster-api-machine-role"
	machinesNamespace     = "openshift-machine-api"
	// NOTE(ltomasbo): Amphora driver supports came on 2.11, but ovn-octavia only supports it after 2.13
	MinOctaviaVersionWithMultipleListeners = "v2.13"
	MinOctaviaVersionWithProviders         = "v2.6"
	KuryrNamespace                         = "openshift-kuryr"
	KuryrConfigMapName                     = "kuryr-config"
	DNSNamespace                           = "openshift-dns"
	DNSServiceName                         = "dns-default"
	// NOTE(ltomasbo): Only OVN octavia driver supported on kuryr
	OVNProvider              = "ovn"
	etcdClientPort           = 2379
	etcdServerToServerPort   = 2380
	dnsPort                  = 53
	apiPort                  = 6443
	routerMetricsPort        = 1936
	kubeCMMetricsPort        = 10257
	kubeSchedulerMetricsPort = 10259
	kubeletMetricsPort       = 10250
)

func GetClusterID(kubeClient crclient.Client) (string, error) {
	cluster := &confv1.Infrastructure{
		TypeMeta:   metav1.TypeMeta{APIVersion: "config.openshift.io/v1", Kind: "Infrastructure"},
		ObjectMeta: metav1.ObjectMeta{Name: InfrastructureCRDName},
	}

	err := kubeClient.Get(context.TODO(), crclient.ObjectKey{Name: InfrastructureCRDName}, cluster)
	if err != nil {
		return "", errors.Wrapf(err, "Failed to get Infrastracture CRD %s", InfrastructureCRDName)
	}
	return cluster.Status.InfrastructureName, nil
}

func GetCloudFromSecret(kubeClient crclient.Client) (clientconfig.Cloud, error) {
	emptyCloud := clientconfig.Cloud{}

	secret := &v1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: CloudsSecret},
	}

	err := kubeClient.Get(context.TODO(), crclient.ObjectKey{Namespace: names.APPLIED_NAMESPACE, Name: CloudsSecret}, secret)
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

func ensureCA(kubeClient crclient.Client) ([]byte, []byte, error) {
	secret := &v1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: names.KURYR_ADMISSION_CONTROLLER_SECRET},
	}
	err := kubeClient.Get(context.TODO(), crclient.ObjectKey{Namespace: KuryrNamespace, Name: names.KURYR_ADMISSION_CONTROLLER_SECRET}, secret)
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

func ensureCertificate(kubeClient crclient.Client, caPEM []byte, privateKey []byte) ([]byte, []byte, error) {
	secret := &v1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: names.KURYR_WEBHOOK_SECRET},
	}
	err := kubeClient.Get(context.TODO(), crclient.ObjectKey{Namespace: KuryrNamespace, Name: names.KURYR_WEBHOOK_SECRET}, secret)
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

func getConfigMap(kubeClient crclient.Client, namespace, name string) (*v1.ConfigMap, error) {
	cm := &v1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	err := kubeClient.Get(context.TODO(), crclient.ObjectKey{Namespace: namespace, Name: name}, cm)
	if err != nil {
		return nil, err
	}
	return cm, nil
}

func getCloudProviderCACert(kubeClient crclient.Client) (string, error) {
	cm, err := getConfigMap(kubeClient, OpenShiftConfigNamespace, UserCABundleConfigMap)
	if err != nil {
		return "", err
	}
	return cm.Data["ca-bundle.pem"], nil
}

func getSavedAnnotation(kubeClient crclient.Client, annotation string) (string, error) {
	cm, err := getConfigMap(kubeClient, KuryrNamespace, KuryrConfigMapName)
	if err != nil {
		return "", err
	}
	return cm.Annotations[annotation], nil
}

func getMasterMachines(kubeClient crclient.Client) (*machineapi.MachineList, error) {
	machineList := &machineapi.MachineList{}
	listOptions := []crclient.ListOption{
		crclient.InNamespace(machinesNamespace),
		crclient.MatchingLabels{masterMachineLabel: "master"},
	}
	err := kubeClient.List(context.TODO(), machineList, listOptions...)
	if err != nil {
		return nil, err
	}
	return machineList, nil
}

func ensureSubnetGatewayGuardSvc(kubeClient crclient.Client, requestedIP string) (clusterIP net.IP, err error) {
	// Check if namespace exists.
	ns := v1.Namespace{}
	err = kubeClient.Get(context.TODO(), crclient.ObjectKey{Name: KuryrNamespace}, &ns)
	if err != nil && apierrors.IsNotFound(err) {
		ns = v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: KuryrNamespace,
			},
		}
		err = kubeClient.Create(context.TODO(), &ns)
	}
	if err != nil {
		return clusterIP, err
	}

	svc := v1.Service{}
	err = kubeClient.Get(context.TODO(), crclient.ObjectKey{
		Name:      "service-subnet-gateway-ip",
		Namespace: KuryrNamespace,
	}, &svc)

	if err != nil && apierrors.IsNotFound(err) {
		svc = v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "service-subnet-gateway-ip",
				Namespace: KuryrNamespace,
			},
			Spec: v1.ServiceSpec{
				ClusterIP: requestedIP, // K8s ignores ""
				Ports:     []v1.ServicePort{{Port: 80}},
			},
		}
		err = kubeClient.Create(context.TODO(), &svc)
	}
	if err == nil {
		clusterIP = net.ParseIP(svc.Spec.ClusterIP)
	}

	return clusterIP, err
}

func getWorkersSubnetFromMasters(client *gophercloud.ServiceClient, kubeClient crclient.Client, clusterID string) (subnets.Subnet, error) {
	empty := subnets.Subnet{}
	machines, err := getMasterMachines(kubeClient)
	if err != nil {
		return empty, err
	}
	for _, machine := range machines.Items {
		// First is good enough, we're guaranteed masters are on a single
		// subnet.
		providerSpec, err := capo.MachineSpecFromProviderSpec(machine.Spec.ProviderSpec)
		if err != nil {
			return empty, err
		}
		if providerSpec.PrimarySubnet != "" {
			return getOpenStackSubnet(client, providerSpec.PrimarySubnet)
		} else {
			// TODO(dulek): `PrimarySubnet` is not set on most basic case, we need to look
			//              through `Networks` in other case. This only covers pretty simple
			//              description of the network-subnet pair to support more complicated
			//              one we'd need a way more complicated code here.
			if len(providerSpec.Networks) != 1 || len(providerSpec.Networks[0].Subnets) != 1 {
				log.Printf("Failed to figure out subnet of Machine %s", machine.Name)
				continue
			}

			subnet := providerSpec.Networks[0].Subnets[0]
			if subnet.UUID != "" {
				return getOpenStackSubnet(client, subnet.UUID)
			}

			return findOpenStackSubnet(client, subnet.Filter.Name, subnet.Filter.Tags)
		}
	}
	// As a last resort we look for it by a tag - older versions were tagging BYON network.
	primaryNetworkTag := clusterID + "-primaryClusterNetwork"
	return findOpenStackSubnetByNetworkTag(client, primaryNetworkTag)
}

// Logs into OpenStack and creates all the resources that are required to run
// Kuryr based on conf NetworkConfigSpec. Basically this includes service
// network and subnet, pods subnetpool, security group and load balancer for
// OpenShift API. Besides that it looks up router and subnet used by OpenShift
// worker nodes (needed to configure Kuryr) and makes sure there's a routing
// between them and created subnets. Also SG rules are added to make sure pod
// subnet can reach nodes and nodes can reach pods and services. The data is
// returned to populate Kuryr's configuration.
func BootstrapKuryr(conf *operv1.NetworkSpec, kubeClient crclient.Client) (*bootstrap.BootstrapResult, error) {
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

	// We cannot rely on the inject-proxy annotation because the CVO, which is
	// responsible to inject the proxy env vars, is not available before CNO.
	proxyConfig := &configv1.Proxy{}
	err = kubeClient.Get(context.TODO(), types.NamespacedName{Name: names.CLUSTER_CONFIG}, proxyConfig)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}

	transport := http.Transport{}
	noProxy := proxyConfig.Status.NoProxy
	httpProxy := proxyConfig.Status.HTTPProxy
	httpsProxy := proxyConfig.Status.HTTPSProxy
	hasProxy := len(httpsProxy) > 0 || len(httpProxy) > 0 || len(noProxy) > 0
	if hasProxy {
		os.Setenv("NO_PROXY", noProxy)
		os.Setenv("HTTP_PROXY", httpProxy)
		os.Setenv("HTTPS_PROXY", httpsProxy)
		// The env vars are not propagated to different libs when not set on
		// main(), so we'll load it directly here and rely on http lib to choose
		// the proxy URL.
		proxyfunc := httpproxy.FromEnvironment().ProxyFunc()
		transport.Proxy = func(req *http.Request) (*url.URL, error) {
			return proxyfunc(req.URL)
		}
		provider.HTTPClient = http.Client{Transport: &transport}

		// Due to an issue in the urllib3 library https://github.com/psf/requests/issues/5939
		// Kuryr will currently default to use http scheme when https is set.
		proxyUrl, err := url.Parse(httpsProxy)
		if err != nil {
			return nil, errors.Wrap(err, "failed to parse cluster-wide proxy https URL")
		}

		if proxyUrl.Scheme == "https" {
			if len(httpProxy) > 0 {
				log.Printf("Kuryr requires proxy to use http scheme. Defaulting proxy to %s", httpProxy)
				httpsProxy = httpProxy
			} else {
				return nil, errors.New("Kuryr currently requires proxy to use http scheme.")
			}
		}
	}

	if userCACert != "" {
		certPool, err := x509.SystemCertPool()
		if err == nil {
			certPool.AppendCertsFromPEM([]byte(userCACert))
			transport.TLSClientConfig = &tls.Config{RootCAs: certPool}
			provider.HTTPClient = http.Client{Transport: &transport}
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

	lbClient, err := openstack.NewLoadBalancerV2(provider, gophercloud.EndpointOpts{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to create Octavia client")
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
	octaviaProvider, err := getSavedAnnotation(kubeClient, names.KuryrOctaviaProviderAnnotation)
	if err != nil && !apierrors.IsNotFound(err) { // Ignore 404, just do the normal discovery then.
		return nil, errors.Wrap(err, "failed to get kuryr-config ConfigMap")
	}
	if octaviaProvider != "" {
		log.Printf("Detected that Kuryr was already configured to use %s LB provider. Making sure to keep it that way.",
			octaviaProvider)
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
					}
				}
			}
		}
	}

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

	svcSubnetName := generateName("kuryr-service-subnet", clusterID)
	openStackSvcCIDR := kc.OpenStackServiceNetwork
	_, openStackSvcNet, _ := net.ParseCIDR(openStackSvcCIDR)
	allocationRanges := iputil.UsableNonOverlappingRanges(*openStackSvcNet, *svcNet)
	// OpenShift will use svcNet range. In allocationRanges we have parts of openStackSvcNet that are not overlapping
	// with svcNet. We will put svcSubnetGatewayIP on the highest usable IP from those ranges. We need to exclude that IP from
	// the ranges we pass to Neutron or it will complain.
	svcSubnetGatewayIP := allocationRanges[len(allocationRanges)-1].End
	allocationRanges[len(allocationRanges)-1].End = iputil.IterateIP4(svcSubnetGatewayIP, -1)

	log.Printf("Looking for existing services subnet with %s CIDR (services from %s) and %s gateway",
		openStackSvcCIDR, svcNet.String(), svcSubnetGatewayIP)
	var svcSubnetId string
	svcSubnet, err := findOpenStackSubnetByDetails(client, svcSubnetName, tag, svcNetId, openStackSvcCIDR, svcSubnetGatewayIP.String())
	if err != nil {
		return nil, errors.Wrap(err, "failed to lookup service subnet")
	}
	if svcSubnet != nil {
		// Even if we use OVN now, we don't want to change the old Amphora-style subnet.
		svcSubnetId = svcSubnet.ID
		log.Printf("Found existing services subnet %s", svcSubnetId)
	} else {
		// No old subnet, either this is fresh installation or 4.8 installed on OVN from the beginning
		if octaviaProvider == "default" {
			// This means we need to create amphora-style inflated subnet
			svcSubnetId, err = createOpenStackSubnet(client, svcSubnetName, tag, svcNetId, openStackSvcCIDR, svcSubnetGatewayIP.String(), allocationRanges)
			if err != nil {
				return nil, errors.Wrap(err, "failed to create service subnet")
			}
		} else {
			// Fresh installation and OVN, we need to lookup or create smaller subnet.
			// We lookup without gateway IP. If we'll find the subnet we'll take it from there.
			// If not, we'll create a Service to reserve an IP and use that one.
			log.Printf("Looking for existing services subnet with %s CIDR", svcNet.String())
			svcSubnet, err = findOpenStackSubnetByDetails(client, svcSubnetName, tag, svcNetId, svcNet.String(), "")
			if err != nil {
				return nil, errors.Wrap(err, "failed to lookup service subnet")
			}
			if svcSubnet != nil {
				svcSubnetId = svcSubnet.ID
				svcSubnetGatewayIP = net.ParseIP(svcSubnet.GatewayIP)
				log.Printf("Found existing services subnet %s", svcSubnetId)
				// Recreate the guard service if needed.
				svcSubnetGatewayIP, err = ensureSubnetGatewayGuardSvc(kubeClient, svcSubnetGatewayIP.String())
				if err != nil {
					return nil, errors.Wrap(err, "failed to create a Service to guard gateway IP on services subnet")
				}
			}
			if len(svcSubnetId) == 0 {
				// We need to reserve an IP for the gateway, so we'll create a dummy Service
				// and use the IP K8s assigns to it.
				svcSubnetGatewayIP, err = ensureSubnetGatewayGuardSvc(kubeClient, "")
				if err != nil {
					return nil, errors.Wrap(err, "failed to create a Service to guard gateway IP on services subnet")
				}
				// We need to create it then
				svcSubnetId, err = createOpenStackSubnet(client, svcSubnetName, tag, svcNetId, svcNet.String(), svcSubnetGatewayIP.String(), nil)
				if err != nil {
					return nil, errors.Wrap(err, "failed to create service subnet")
				}
			}
		}
	}

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

	workerSubnet, err := getWorkersSubnetFromMasters(client, kubeClient, clusterID)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find worker nodes subnet")
	}
	log.Printf("Found worker nodes subnet %s", workerSubnet.ID)

	mtu, err := getOpenStackNetworkMTU(client, workerSubnet.NetworkID)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get network MTU")
	}
	log.Printf("Found Nodes Network MTU %d", mtu)

	router, err := ensureOpenStackRouter(client, generateName("external-router", clusterID), tag, workerSubnet.NetworkID)
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

	if !lookupOpenStackPort(ps, workerSubnet.ID) {
		log.Printf("Adding worker nodes %s subnet to the router", workerSubnet.ID)
		err = ensureOpenStackRouterInterface(client, routerId, &workerSubnet.ID, nil)
		if err != nil {
			return nil, errors.Wrap(err, "failed to create worker nodes subnet router interface")
		}
	}

	if !lookupOpenStackPort(ps, svcSubnetId) {
		log.Printf("Ensuring service subnet router port with %s IP", svcSubnetGatewayIP.String())
		portId, err := ensureOpenStackPort(client, generateName("kuryr-service-subnet-router-port", clusterID), tag,
			svcNetId, svcSubnetId, svcSubnetGatewayIP.String())
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
	if err != nil {
		return nil, errors.Wrap(err, "failed to find pods security group")
	}
	log.Printf("Pods security group %s present", podSgId)

	type sgRule struct {
		sgId     string
		cidr     string
		minPort  int
		maxPort  int
		protocol rules.RuleProtocol
	}

	var sgRules = []sgRule{
		{podSgId, "0.0.0.0/0", 0, 0, rules.ProtocolTCP},
		{masterSgId, openStackSvcCIDR, etcdClientPort, etcdClientPort, rules.ProtocolTCP},
		{masterSgId, openStackSvcCIDR, apiPort, apiPort, rules.ProtocolTCP},
		// NOTE (maysamacedo): Splitting etcd sg port ranges in different
		// rules to avoid the issue of constant leader election changes
		{masterSgId, workerSubnet.CIDR, etcdClientPort, etcdClientPort, rules.ProtocolTCP},
		{masterSgId, workerSubnet.CIDR, etcdServerToServerPort, etcdServerToServerPort, rules.ProtocolTCP},
	}

	var decommissionedRules = []sgRule{
		{podSgId, workerSubnet.CIDR, 0, 0, ""},
		{masterSgId, openStackSvcCIDR, etcdClientPort, etcdServerToServerPort, rules.ProtocolTCP},
		// NOTE(maysamacedo): This sg rule is created by the installer. We need to remove it and
		// create two more, each with a unique port from the range.
		{masterSgId, workerSubnet.CIDR, etcdClientPort, etcdServerToServerPort, rules.ProtocolTCP},
	}

	for _, cidr := range podSubnetCidrs {
		sgRules = append(sgRules,
			sgRule{masterSgId, cidr, etcdClientPort, etcdClientPort, rules.ProtocolTCP},
			sgRule{masterSgId, cidr, dnsPort, dnsPort, rules.ProtocolTCP},
			sgRule{masterSgId, cidr, dnsPort, dnsPort, rules.ProtocolUDP},
			sgRule{workerSgId, cidr, dnsPort, dnsPort, rules.ProtocolTCP},
			sgRule{workerSgId, cidr, dnsPort, dnsPort, rules.ProtocolUDP},
			sgRule{workerSgId, cidr, routerMetricsPort, routerMetricsPort, rules.ProtocolTCP},
			sgRule{masterSgId, cidr, kubeCMMetricsPort, kubeCMMetricsPort, rules.ProtocolTCP},
			sgRule{masterSgId, cidr, kubeSchedulerMetricsPort, kubeSchedulerMetricsPort, rules.ProtocolTCP},
			sgRule{masterSgId, cidr, kubeletMetricsPort, kubeletMetricsPort, rules.ProtocolTCP},
			sgRule{workerSgId, cidr, kubeletMetricsPort, kubeletMetricsPort, rules.ProtocolTCP},
			// NOTE(dulek): I honestly don't know why this is so broad range, but I took it from SGs installer sets
			//              for workers and masters. In general point was to open 9192 so that metrics of
			//              cluster-autoscaler-operator were rechable.
			sgRule{masterSgId, cidr, 9000, 9999, rules.ProtocolTCP},
			sgRule{masterSgId, cidr, 9000, 9999, rules.ProtocolUDP},
			sgRule{workerSgId, cidr, 9000, 9999, rules.ProtocolTCP},
			sgRule{workerSgId, cidr, 9000, 9999, rules.ProtocolUDP},
		)

		decommissionedRules = append(decommissionedRules,
			sgRule{masterSgId, cidr, 0, 0, ""},
			sgRule{workerSgId, cidr, 0, 0, ""},
		)
	}

	log.Print("Allowing required traffic")
	for _, rule := range sgRules {
		err = ensureOpenStackSgRule(client, rule.sgId, rule.cidr, rule.minPort, rule.maxPort, rule.protocol)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to add %s rule opening traffic from %s in security group %s on ports %d:%d",
				rule.protocol, rule.cidr, rule.sgId, rule.minPort, rule.maxPort)
		}
	}
	log.Print("All requried traffic allowed")

	// It may happen that we tightened some SG rules in an upgrade, we need to make sure to remove the ones that are
	// not expected anymore.
	log.Print("Removing old SG rules")
	existingRules, err := listOpenStackSgRules(client, tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list SG rules")
	}

	for _, existingRule := range existingRules {
		// Need to convert to "our" format for easy comparisons.
		rule := sgRule{existingRule.SecGroupID, existingRule.RemoteIPPrefix, existingRule.PortRangeMin,
			existingRule.PortRangeMax, rules.RuleProtocol(existingRule.Protocol)}
		for _, decommissionedRule := range decommissionedRules {
			if decommissionedRule == rule {
				log.Printf("Removing decommisioned rule %s (%s, %d, %d, %s) from SG %s", existingRule.ID,
					existingRule.RemoteIPPrefix, existingRule.PortRangeMin, existingRule.PortRangeMax,
					existingRule.Protocol, existingRule.SecGroupID)
				err = rules.Delete(client, existingRule.ID).ExtractErr()
				if err != nil {
					return nil, errors.Wrapf(err, "Could not delete SG rule %s", existingRule.ID)
				}
				break
			}
		}
	}
	log.Print("All old SG rules removed")

	log.Print("Ensuring certificates")
	ca, key, err := ensureCA(kubeClient)
	if err != nil {
		return nil, errors.Wrap(err, "failed to ensure CA")
	}
	webhookCert, webhookKey, err := ensureCertificate(kubeClient, ca, key)
	if err != nil {
		return nil, errors.Wrap(err, "failed to ensure Certificate")
	}

	octaviaVersion, err := getSavedAnnotation(kubeClient, names.KuryrOctaviaVersionAnnotation)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, errors.Wrap(err, "failed to get kuryr-config ConfigMap")
	}

	maxOctaviaVersion, err := getMaxOctaviaAPIVersion(lbClient)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get max octavia api version")
	}

	// In case the Kuryr config-map is annotated with an Octavia version different
	// than the current Octavia version, and older than the version that multiple
	// listeners becomes available and the Octavia provider is amphora, an Octavia
	// upgrade happened and UDP listeners are now allowed to be created.
	// By recreating the OpenShift DNS service a new load balancer amphora is in
	// place with all required listeners.
	log.Print("Checking Octavia upgrade happened")
	if octaviaVersion != "" && octaviaProvider == "default" {
		savedOctaviaVersion := semver.MustParse(octaviaVersion)
		multipleListenersVersion := semver.MustParse(MinOctaviaVersionWithMultipleListeners)
		if !savedOctaviaVersion.Equal(maxOctaviaVersion) && savedOctaviaVersion.LessThan(multipleListenersVersion) && octaviaMultipleListenersSupport {
			dnsService := &v1.Service{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
				ObjectMeta: metav1.ObjectMeta{Name: DNSServiceName, Namespace: DNSNamespace},
			}
			err := kubeClient.Delete(context.TODO(), dnsService)
			if err != nil {
				return nil, errors.Wrapf(err, "Failed to delete %s Service", DNSServiceName)
			}
		}
	}
	octaviaVersion = maxOctaviaVersion.Original()

	infraConfig, err := platform.BootstrapInfra(kubeClient)
	if err != nil {
		return nil, err
	}

	log.Print("Kuryr bootstrap finished")

	res := bootstrap.BootstrapResult{
		Infra: *infraConfig,
		Kuryr: bootstrap.KuryrBootstrapResult{
			ServiceSubnet:            svcSubnetId,
			PodSubnetpool:            podSubnetpoolId,
			WorkerNodesRouter:        routerId,
			WorkerNodesSubnets:       []string{workerSubnet.ID},
			PodsNetworkMTU:           mtu,
			PodSecurityGroups:        []string{podSgId},
			ExternalNetwork:          externalNetwork,
			ClusterID:                clusterID,
			OctaviaProvider:          octaviaProvider,
			OctaviaMultipleListeners: octaviaMultipleListenersSupport,
			OctaviaVersion:           octaviaVersion,
			OpenStackCloud:           cloud,
			WebhookCA:                b64.StdEncoding.EncodeToString(ca),
			WebhookCAKey:             b64.StdEncoding.EncodeToString(key),
			WebhookKey:               b64.StdEncoding.EncodeToString(webhookKey),
			WebhookCert:              b64.StdEncoding.EncodeToString(webhookCert),
			UserCACert:               userCACert,
			HttpProxy:                httpProxy,
			HttpsProxy:               httpsProxy,
			NoProxy:                  noProxy,
		}}
	return &res, nil
}
