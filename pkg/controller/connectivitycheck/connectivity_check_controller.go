package connectivitycheck

import (
	"context"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/api/operatorcontrolplane/v1alpha1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	configv1listers "github.com/openshift/client-go/config/listers/config/v1"
	operatorcontrolplaneclient "github.com/openshift/client-go/operatorcontrolplane/clientset/versioned"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/connectivitycheckcontroller"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/genericoperatorclient"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type OVNKubernetesConnectivityCheckController interface {
	connectivitycheckcontroller.ConnectivityCheckController
}

func NewOVNKubernetesConnectivityCheckController(
	kubeClient kubernetes.Interface,
	operatorClient v1helpers.OperatorClient,
	operatorcontrolplaneClient *operatorcontrolplaneclient.Clientset,
	apiextensionsClient *apiextensionsclient.Clientset,
	kubeInformersForNamespaces v1helpers.KubeInformersForNamespaces,
	configInformers configinformers.SharedInformerFactory,
	apiextensionsInformers apiextensionsinformers.SharedInformerFactory,
	recorder events.Recorder,
) OVNKubernetesConnectivityCheckController {
	c := ovnKubernetesConnectivityCheckController{
		ConnectivityCheckController: connectivitycheckcontroller.NewConnectivityCheckController(
			"openshift-ovn-kubernetes",
			operatorClient,
			operatorcontrolplaneClient,
			apiextensionsClient,
			apiextensionsInformers,
			[]factory.Informer{
				operatorClient.Informer(),
				kubeInformersForNamespaces.InformersFor("openshift-ovn-kubernetes").Core().V1().Pods().Informer(),
				kubeInformersForNamespaces.InformersFor("openshift-kube-apiserver").Core().V1().Endpoints().Informer(),
				kubeInformersForNamespaces.InformersFor("openshift-kube-apiserver").Core().V1().Services().Informer(),
				configInformers.Config().V1().Infrastructures().Informer(),
			},
			recorder,
			true,
		),
	}
	generator := &connectivityCheckTemplateProvider{
		operatorClient:             operatorClient,
		operatorcontrolplaneClient: operatorcontrolplaneClient,
		endpointsLister:            kubeInformersForNamespaces.InformersFor("openshift-kube-apiserver").Core().V1().Endpoints().Lister(),
		serviceLister:              kubeInformersForNamespaces.InformersFor("openshift-kube-apiserver").Core().V1().Services().Lister(),
		podLister:                  kubeInformersForNamespaces.InformersFor("openshift-ovn-kubernetes").Core().V1().Pods().Lister(),
		infrastructureLister:       configInformers.Config().V1().Infrastructures().Lister(),
	}
	return c.WithPodNetworkConnectivityCheckFn(generator.generate)
}

type ovnKubernetesConnectivityCheckController struct {
	connectivitycheckcontroller.ConnectivityCheckController
}

type connectivityCheckTemplateProvider struct {
	operatorClient             v1helpers.OperatorClient
	operatorcontrolplaneClient *operatorcontrolplaneclient.Clientset
	endpointsLister            corev1listers.EndpointsLister
	serviceLister              corev1listers.ServiceLister
	podLister                  corev1listers.PodLister
	infrastructureLister       configv1listers.InfrastructureLister
}

func (c *connectivityCheckTemplateProvider) generate(ctx context.Context, syncContext factory.SyncContext) ([]*v1alpha1.PodNetworkConnectivityCheck, error) {
	var templates []*v1alpha1.PodNetworkConnectivityCheck
	// kas service IP
	templates = append(templates, c.getTemplatesForKubernetesServiceMonitorService(syncContext.Recorder())...)
	// kas default service IP
	templates = append(templates, c.getTemplatesForKubernetesDefaultServiceCheck(syncContext.Recorder())...)
	// each kas endpoint IP
	templates = append(templates, c.getTemplatesForKubernetesServiceEndpointsChecks(syncContext.Recorder())...)
	// each api load balancer hostname
	templates = append(templates, c.getTemplatesForApiLoadBalancerChecks(syncContext.Recorder())...)

	pods, err := c.podLister.List(labels.Set{"app": "ovnkube-node"}.AsSelector())
	if err != nil {
		syncContext.Recorder().Warningf("EndpointDetectionFailure", "failed to list openshift-apiserver pods: %v", err)
		return nil, nil
	}

	var checks []*v1alpha1.PodNetworkConnectivityCheck
	for _, pod := range pods {
		if len(pod.Spec.NodeName) == 0 {
			// apiserver pod hasn't been assigned a node yet, skip
			continue
		}
		for _, template := range templates {
			check := template.DeepCopy()
			WithSource("ovnkube-node-" + pod.Spec.NodeName)(check)
			check.Spec.SourcePod = pod.Name
			checks = append(checks, check)
		}
	}
	return checks, nil
}

func (c *connectivityCheckTemplateProvider) getTemplatesForKubernetesDefaultServiceCheck(recorder events.Recorder) []*v1alpha1.PodNetworkConnectivityCheck {
	var templates []*v1alpha1.PodNetworkConnectivityCheck
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if len(host) == 0 || len(port) == 0 {
		recorder.Warningf("EndpointDetectionFailure", "unable to determine kubernetes service endpoint: in-cluster configuration not found")
		return templates
	}
	return append(templates, NewPodNetworkConnectivityCheckTemplate(net.JoinHostPort(host, port), "openshift-ovn-kubernetes", withTarget("kubernetes-default-service", "cluster")))
}

func (c *connectivityCheckTemplateProvider) getTemplatesForKubernetesServiceMonitorService(recorder events.Recorder) []*v1alpha1.PodNetworkConnectivityCheck {
	var templates []*v1alpha1.PodNetworkConnectivityCheck
	for _, address := range c.listAddressesForKubernetesServiceMonitorService(recorder) {
		templates = append(templates, NewPodNetworkConnectivityCheckTemplate(address, "openshift-ovn-kubernetes", withTarget("kubernetes-apiserver-service", "cluster")))
	}
	return templates
}

func (c *connectivityCheckTemplateProvider) listAddressesForKubernetesServiceMonitorService(recorder events.Recorder) []string {
	service, err := c.serviceLister.Services("openshift-kube-apiserver").Get("apiserver")
	if err != nil {
		recorder.Warningf("EndpointDetectionFailure", "unable to determine openshift-kube-apiserver apiserver service endpoint: %v", err)
		return nil
	}
	for _, port := range service.Spec.Ports {
		if port.TargetPort.IntValue() == 6443 {
			return []string{net.JoinHostPort(service.Spec.ClusterIP, strconv.Itoa(int(port.Port)))}
		}
	}
	return []string{net.JoinHostPort(service.Spec.ClusterIP, "443")}
}

func (c *connectivityCheckTemplateProvider) getTemplatesForKubernetesServiceEndpointsChecks(recorder events.Recorder) []*v1alpha1.PodNetworkConnectivityCheck {
	var templates []*v1alpha1.PodNetworkConnectivityCheck
	addresses, err := c.listAddressesForKubeAPIServerServiceEndpoints(recorder)
	if err != nil {
		recorder.Warningf("EndpointDetectionFailure", "unable to determine openshift-kube-apiserver apiserver endpoints: %v", err)
		return nil
	}

	for _, address := range addresses {
		templates = append(templates, NewPodNetworkConnectivityCheckTemplate(net.JoinHostPort(address.hostName, address.port), "openshift-ovn-kubernetes", withTarget("kubernetes-apiserver-endpoint", address.nodeName)))
	}
	return templates
}

// listAddressesForKubeAPIServerServiceEndpoints returns kas api service endpoints ip
func (c *connectivityCheckTemplateProvider) listAddressesForKubeAPIServerServiceEndpoints(recorder events.Recorder) ([]endpointInfo, error) {
	var results []endpointInfo
	endpoints, err := c.endpointsLister.Endpoints("openshift-kube-apiserver").Get("apiserver")
	if err != nil {
		return nil, err
	}
	for _, subset := range endpoints.Subsets {
		for _, address := range subset.Addresses {
			for _, port := range subset.Ports {
				results = append(results, endpointInfo{
					hostName: address.IP,
					port:     strconv.Itoa(int(port.Port)),
					nodeName: *address.NodeName,
				})
			}
		}
	}
	return results, nil
}

func (c *connectivityCheckTemplateProvider) getTemplatesForApiLoadBalancerChecks(recorder events.Recorder) []*v1alpha1.PodNetworkConnectivityCheck {
	var templates []*v1alpha1.PodNetworkConnectivityCheck
	infrastructure, err := c.infrastructureLister.Get("cluster")
	if err != nil {
		recorder.Warningf("EndpointDetectionFailure", "error detecting api load balancer endpoints: %v", err)
		return nil
	}

	apiUrl, err := url.Parse(infrastructure.Status.APIServerURL)
	if err != nil {
		recorder.Warningf("EndpointDetectionFailure", "error detecting external api load balancer endpoint: %v", err)

	} else {
		templates = append(templates, NewPodNetworkConnectivityCheckTemplate(apiUrl.Host, "openshift-ovn-kubernetes", withTarget("load-balancer", "api-external")))
	}

	apiInternalUrl, err := url.Parse(infrastructure.Status.APIServerInternalURL)
	if err != nil {
		recorder.Warningf("EndpointDetectionFailure", "error detecting internal api load balancer endpoint: %v", err)
	} else {
		templates = append(templates, NewPodNetworkConnectivityCheckTemplate(apiInternalUrl.Host, "openshift-ovn-kubernetes", withTarget("load-balancer", "api-internal")))
	}
	return templates
}

type endpointInfo struct {
	hostName string
	port     string
	nodeName string
}

func withTarget(label, nodeName string) func(check *v1alpha1.PodNetworkConnectivityCheck) {
	return WithTarget(label + "-" + nodeName)
}

func Add(mgr manager.Manager, status *statusmanager.StatusManager) error {
	ctx := context.TODO()
	kubeConfig := mgr.GetConfig()
	protoKubeConfig := mgr.GetConfig()
	protoKubeConfig.ContentType = "application/vnd.kubernetes.protobuf"
	eventRecorder := &loggingRecorder{}
	kubeClient, err := kubernetes.NewForConfig(protoKubeConfig)
	if err != nil {
		return err
	}
	configClient, err := configv1client.NewForConfig(kubeConfig)
	if err != nil {
		return err
	}
	operatorClient, _, err := genericoperatorclient.NewClusterScopedOperatorClient(kubeConfig, operatorv1.GroupVersion.WithResource("openshiftapiservers"))
	if err != nil {
		return err
	}
	operatorcontrolplaneClient, err := operatorcontrolplaneclient.NewForConfig(kubeConfig)
	if err != nil {
		return err
	}
	apiextensionsClient, err := apiextensionsclient.NewForConfig(kubeConfig)
	if err != nil {
		return err
	}
	apiextensionsInformers := apiextensionsinformers.NewSharedInformerFactory(apiextensionsClient, 10*time.Minute)
	kubeInformersForNamespaces := v1helpers.NewKubeInformersForNamespaces(kubeClient,
		"",
		"openshift-config",
		"openshift-config-managed",
		"openshift-cluster-network-operator",
		"openshift-ovn-kubernetes",
		metav1.NamespaceSystem,
		"openshift-kube-apiserver",
		"openshift-oauth-apiserver",
	)
	configInformers := configinformers.NewSharedInformerFactory(configClient, 10*time.Minute)
	connectivityCheckController := NewOVNKubernetesConnectivityCheckController(
		kubeClient,
		operatorClient,
		operatorcontrolplaneClient,
		apiextensionsClient,
		kubeInformersForNamespaces,
		configInformers,
		apiextensionsInformers,
		eventRecorder,
	)

	go connectivityCheckController.Run(ctx, 1)

	<-ctx.Done()
	return nil
}
