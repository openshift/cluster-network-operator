package connectivitycheck

import (
	"context"
	"fmt"
	"k8s.io/utils/clock"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	applyconfigv1 "github.com/openshift/client-go/config/applyconfigurations/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	configv1listers "github.com/openshift/client-go/config/listers/config/v1"
	applyconfigv1alpha1 "github.com/openshift/client-go/operatorcontrolplane/applyconfigurations/operatorcontrolplane/v1alpha1"
	operatorcontrolplaneclient "github.com/openshift/client-go/operatorcontrolplane/clientset/versioned"
	operatorcontrolplaneinformers "github.com/openshift/client-go/operatorcontrolplane/informers/externalversions"
	"github.com/openshift/cluster-network-operator/pkg/controller/eventrecorder"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/connectivitycheckcontroller"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/genericoperatorclient"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	v1 "k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	applyconfigmetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
)

type NetworkConnectivityCheckController interface {
	connectivitycheckcontroller.ConnectivityCheckController
}

// NetworkConnectivyCheckController consumes a series of clients, informers and a recorders.
// With those objects it generates a series of templates for creating PodNetworkConnectivityChecks CRs,
// in particular:
// Checks between network-check-source pod and every kube apiserver service and endpoints
// Checks between network-check-source pod and every openshift apiserver service and endpoints
// Checks between network-check-source pod and every LB
// Checks between network-check-source pod and network-check-target service and endpoints this being managed by a Daemonset
func NewNetworkConnectivityCheckController(
	operatorClient v1helpers.OperatorClient,
	configClient *configv1client.Clientset,
	operatorcontrolplaneClient *operatorcontrolplaneclient.Clientset,
	apiextensionsClient *apiextensionsclient.Clientset,
	kubeInformersForNamespaces v1helpers.KubeInformersForNamespaces,
	configInformers configinformers.SharedInformerFactory,
	apiextensionsInformers apiextensionsinformers.SharedInformerFactory,
	operatorcontrolplaneInformers operatorcontrolplaneinformers.SharedInformerFactory,
	recorder events.Recorder,
) NetworkConnectivityCheckController {
	c := networkConnectivityCheckController{
		ConnectivityCheckController: connectivitycheckcontroller.NewConnectivityCheckController(
			"openshift-network-diagnostics",
			operatorClient,
			operatorcontrolplaneClient,
			apiextensionsClient,
			apiextensionsInformers,
			configInformers,
			[]factory.Informer{
				operatorClient.Informer(),
				kubeInformersForNamespaces.InformersFor("openshift-network-diagnostics").Core().V1().Pods().Informer(),
				kubeInformersForNamespaces.InformersFor("openshift-network-diagnostics").Core().V1().Endpoints().Informer(),
				kubeInformersForNamespaces.InformersFor("openshift-network-diagnostics").Core().V1().Services().Informer(),
				kubeInformersForNamespaces.InformersFor("openshift-kube-apiserver").Core().V1().Endpoints().Informer(),
				kubeInformersForNamespaces.InformersFor("openshift-kube-apiserver").Core().V1().Services().Informer(),
				kubeInformersForNamespaces.InformersFor("openshift-apiserver").Core().V1().Endpoints().Informer(),
				kubeInformersForNamespaces.InformersFor("openshift-apiserver").Core().V1().Services().Informer(),
				configInformers.Config().V1().Infrastructures().Informer(),
			},
			recorder,
			true,
		),
	}
	c.ConnectivityCheckController = c.WithReapOldConnectivityCheck(operatorcontrolplaneInformers)
	generator := &connectivityCheckTemplateProvider{
		operatorClient:                    operatorClient,
		configClient:                      configClient,
		operatorcontrolplaneClient:        operatorcontrolplaneClient,
		diagnosticsPodLister:              kubeInformersForNamespaces.InformersFor("openshift-network-diagnostics").Core().V1().Pods().Lister(),
		diagnosticsEndpointsLister:        kubeInformersForNamespaces.InformersFor("openshift-network-diagnostics").Core().V1().Endpoints().Lister(),
		diagnosticsServiceLister:          kubeInformersForNamespaces.InformersFor("openshift-network-diagnostics").Core().V1().Services().Lister(),
		kubeAPIServerEndpointsLister:      kubeInformersForNamespaces.InformersFor("openshift-kube-apiserver").Core().V1().Endpoints().Lister(),
		kubeAPIServerServiceLister:        kubeInformersForNamespaces.InformersFor("openshift-kube-apiserver").Core().V1().Services().Lister(),
		defaultServiceLister:              kubeInformersForNamespaces.InformersFor("default").Core().V1().Services().Lister(),
		openshiftAPIServerEndpointsLister: kubeInformersForNamespaces.InformersFor("openshift-apiserver").Core().V1().Endpoints().Lister(),
		openshiftAPIServerServiceLister:   kubeInformersForNamespaces.InformersFor("openshift-apiserver").Core().V1().Services().Lister(),
		nodeLister:                        kubeInformersForNamespaces.InformersFor("").Core().V1().Nodes().Lister(),
		infrastructureLister:              configInformers.Config().V1().Infrastructures().Lister(),
	}

	return c.WithPodNetworkConnectivityCheckApplyFn(generator.generate)
}

type networkConnectivityCheckController struct {
	connectivitycheckcontroller.ConnectivityCheckController
}

type connectivityCheckTemplateProvider struct {
	operatorClient                    v1helpers.OperatorClient
	configClient                      *configv1client.Clientset
	operatorcontrolplaneClient        *operatorcontrolplaneclient.Clientset
	diagnosticsPodLister              corev1listers.PodLister
	diagnosticsEndpointsLister        corev1listers.EndpointsLister
	diagnosticsServiceLister          corev1listers.ServiceLister
	kubeAPIServerEndpointsLister      corev1listers.EndpointsLister
	kubeAPIServerServiceLister        corev1listers.ServiceLister
	defaultServiceLister              corev1listers.ServiceLister
	openshiftAPIServerEndpointsLister corev1listers.EndpointsLister
	openshiftAPIServerServiceLister   corev1listers.ServiceLister
	nodeLister                        corev1listers.NodeLister
	infrastructureLister              configv1listers.InfrastructureLister
	connectivityChecksStatus          metav1.Condition
}

func (c *connectivityCheckTemplateProvider) generate(ctx context.Context, syncContext factory.SyncContext) ([]*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration, error) {
	var templates []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration
	// kas default service IP
	templates = append(templates, c.getTemplatesForKubernetesDefaultServiceCheck(syncContext.Recorder())...)
	if hcpCfg := hypershift.NewHyperShiftConfig(); !hcpCfg.Enabled {
		// In hypershift, the following services/endpoints are not present in the hosted cluster
		// kas service IP
		templates = append(templates, c.getTemplatesForKubernetesServiceMonitorService(syncContext.Recorder())...)
		// each kas endpoint IP
		templates = append(templates, c.getTemplatesForKubernetesServiceEndpointsChecks(syncContext.Recorder())...)
		// oas service IP
		templates = append(templates, c.getTemplatesForOpenShiftAPIServerServiceCheck(syncContext.Recorder())...)
		// each oas endpoint IP
		templates = append(templates, c.getTemplatesForOpenShiftAPIServerServiceEndpointsChecks(syncContext.Recorder())...)
	}
	// each api load balancer hostname
	templates = append(templates, c.getTemplatesForAPILoadBalancerChecks(syncContext.Recorder())...)
	// generic pod service IP
	templates = append(templates, c.getTemplatesForGenericPodServiceCheck(syncContext.Recorder())...)
	// each generic pod endpoint IP
	templates = append(templates, c.getTemplatesForGenericPodServiceEndpointsChecks(syncContext.Recorder())...)

	pods, err := c.diagnosticsPodLister.List(labels.Set{"app": "network-check-source"}.AsSelector())
	if err != nil {
		syncContext.Recorder().Warningf("EndpointDetectionFailure", "failed to list network-check-source pods: %v", err)
		return nil, nil
	}

	var checks []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration
	var anySourcePodScheduled bool
	nodes := make(map[string]*v1.Node)
	nodeApiVersion := "v1"
	nodeKind := "Node"
	for _, pod := range pods {
		if pod.Spec.NodeName == "" {
			// network-checker pod hasn't been assigned a node yet, skip
			continue
		}
		anySourcePodScheduled = true
		var node *v1.Node
		var ok bool
		if node, ok = nodes[pod.Spec.NodeName]; !ok {
			var err error
			node, err = c.nodeLister.Get(pod.Spec.NodeName)
			if err != nil {
				return nil, err
			}
			nodes[node.Name] = node
		}
		for _, template := range templates {
			check := copySpecFields(template)
			WithSource("network-check-source-" + strings.Split(pod.Spec.NodeName, ".")[0])(check)
			check.Spec.SourcePod = &pod.Name
			nodeUID := node.GetUID()
			nodeName := node.GetName()
			nodeRef := applyconfigmetav1.OwnerReferenceApplyConfiguration{
				APIVersion: &nodeApiVersion,
				Kind:       &nodeKind,
				UID:        &nodeUID,
				Name:       &nodeName,
			}
			check.OwnerReferences = append(check.OwnerReferences, nodeRef)
			checks = append(checks, check)
		}
	}

	currentStatus := metav1.Condition{
		Type:   names.NetworkDiagnosticsAvailableCondition,
		Status: metav1.ConditionTrue,
		Reason: "AsExpected",
	}
	if len(pods) == 0 || !anySourcePodScheduled {
		currentStatus.Status = metav1.ConditionFalse
		currentStatus.Reason = "NoSources"
		currentStatus.Message = "No source pods available"
	} else if len(templates) == 0 {
		currentStatus.Status = metav1.ConditionFalse
		currentStatus.Reason = "NoTargets"
		currentStatus.Message = "No targets available"
	}

	if c.connectivityChecksStatus != currentStatus {
		condition := applyconfigmetav1.Condition().
			WithType(currentStatus.Type).
			WithStatus(currentStatus.Status).
			WithObservedGeneration(currentStatus.ObservedGeneration).
			WithLastTransitionTime(metav1.NewTime(time.Now())).
			WithReason(currentStatus.Reason).
			WithMessage(currentStatus.Message)
		netConfig := applyconfigv1.Network(names.CLUSTER_CONFIG).WithStatus(applyconfigv1.NetworkStatus().WithConditions(condition))
		_, err := c.configClient.ConfigV1().Networks().Apply(context.TODO(), netConfig, metav1.ApplyOptions{
			Force:        true,
			FieldManager: "cluster-network-operator/connectivity-check-controller",
		})
		if err != nil {
			return nil, err
		}
		c.connectivityChecksStatus = currentStatus
	}

	return checks, nil
}

func (c *connectivityCheckTemplateProvider) getTemplatesForKubernetesDefaultServiceCheck(recorder events.Recorder) []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration {
	var templates []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration
	service, err := c.defaultServiceLister.Services("default").Get("kubernetes")
	if err != nil {
		recorder.Warningf("EndpointDetectionFailure", "unable to determine kubernetes default service endpoint: %v", err)
		return templates
	}

	port := "443"
	for i, ip := range service.Spec.ClusterIPs {
		templates = append(templates, NewPodNetworkConnectivityCheckTemplate(net.JoinHostPort(ip, port), "openshift-network-diagnostics", withTarget("kubernetes-default-service", "cluster-"+strconv.Itoa(i))))
	}
	return templates
}

func (c *connectivityCheckTemplateProvider) getTemplatesForKubernetesServiceMonitorService(recorder events.Recorder) []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration {
	var templates []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration
	for _, address := range c.listAddressesForKubernetesServiceMonitorService(recorder) {
		templates = append(templates, NewPodNetworkConnectivityCheckTemplate(address, "openshift-network-diagnostics", withTarget("kubernetes-apiserver-service", "cluster")))
	}
	return templates
}

func (c *connectivityCheckTemplateProvider) listAddressesForKubernetesServiceMonitorService(recorder events.Recorder) []string {
	service, err := c.kubeAPIServerServiceLister.Services("openshift-kube-apiserver").Get("apiserver")
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

func (c *connectivityCheckTemplateProvider) getTemplatesForKubernetesServiceEndpointsChecks(recorder events.Recorder) []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration {
	var templates []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration
	addresses, err := c.listAddressesForKubeAPIServerServiceEndpoints(recorder)
	if err != nil {
		recorder.Warningf("EndpointDetectionFailure", "unable to determine openshift-kube-apiserver apiserver endpoints: %v", err)
		return nil
	}

	for _, address := range addresses {
		templates = append(templates, NewPodNetworkConnectivityCheckTemplate(net.JoinHostPort(address.hostName, address.port), "openshift-network-diagnostics", withTarget("kubernetes-apiserver-endpoint", strings.Split(address.nodeName, ".")[0])))
	}
	return templates
}

// listAddressesForKubeAPIServerServiceEndpoints returns kas api service endpoints ip
func (c *connectivityCheckTemplateProvider) listAddressesForKubeAPIServerServiceEndpoints(recorder events.Recorder) ([]endpointInfo, error) {
	var results []endpointInfo
	endpoints, err := c.kubeAPIServerEndpointsLister.Endpoints("openshift-kube-apiserver").Get("apiserver")
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

func (c *connectivityCheckTemplateProvider) getTemplatesForOpenShiftAPIServerServiceCheck(recorder events.Recorder) []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration {
	var templates []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration
	ips, err := c.listAddressesForOpenShiftAPIServerService(recorder)
	if err != nil {
		recorder.Warningf("EndpointDetectionFailure", "unable to determine openshift-apiserver apiserver service: %v", err)
		return nil
	}
	for _, address := range ips {
		templates = append(templates, NewPodNetworkConnectivityCheckTemplate(address,
			"openshift-network-diagnostics",
			withTarget("openshift-apiserver-service", "cluster"),
		))
	}
	return templates
}

func (c *connectivityCheckTemplateProvider) listAddressesForOpenShiftAPIServerService(recorder events.Recorder) ([]string, error) {
	service, err := c.openshiftAPIServerServiceLister.Services("openshift-apiserver").Get("api")
	if err != nil {
		return nil, err
	}
	for _, port := range service.Spec.Ports {
		if port.TargetPort.IntValue() == 6443 {
			return []string{net.JoinHostPort(service.Spec.ClusterIP, strconv.Itoa(int(port.Port)))}, nil
		}
	}
	return []string{net.JoinHostPort(service.Spec.ClusterIP, "443")}, nil
}

func (c *connectivityCheckTemplateProvider) getTemplatesForOpenShiftAPIServerServiceEndpointsChecks(recorder events.Recorder) []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration {
	var templates []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration
	addresses, err := c.listAddressesForOpenShiftAPIServerServiceEndpoints(recorder)
	if err != nil {
		recorder.Warningf("EndpointDetectionFailure", "unable to determine openshift-apiserver apiserver service endpoints: %v", err)
		return nil
	}
	for _, address := range addresses {
		targetEndpoint := net.JoinHostPort(address.hostName, address.port)
		templates = append(templates, NewPodNetworkConnectivityCheckTemplate(targetEndpoint, "openshift-network-diagnostics", withTarget("openshift-apiserver-endpoint", strings.Split(address.nodeName, ".")[0])))
	}
	return templates
}

// listAddressesForOpenShiftAPIServerServiceEndpoints returns oas api service endpoints ip
func (c *connectivityCheckTemplateProvider) listAddressesForOpenShiftAPIServerServiceEndpoints(recorder events.Recorder) ([]endpointInfo, error) {
	endpoints, err := c.openshiftAPIServerEndpointsLister.Endpoints("openshift-apiserver").Get("api")
	if err != nil {
		return nil, err
	}
	if len(endpoints.Subsets) == 0 || len(endpoints.Subsets[0].Ports) == 0 {
		return nil, fmt.Errorf("no openshift-apiserver api endpoints found")
	}
	port := strconv.Itoa(int(endpoints.Subsets[0].Ports[0].Port))
	var results []endpointInfo
	for _, address := range endpoints.Subsets[0].Addresses {
		results = append(results, endpointInfo{
			hostName: address.IP,
			port:     port,
			nodeName: *address.NodeName,
		})
	}
	return results, nil
}
func (c *connectivityCheckTemplateProvider) getTemplatesForGenericPodServiceCheck(recorder events.Recorder) []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration {
	var templates []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration
	return append(templates, NewPodNetworkConnectivityCheckTemplate("network-check-target:80", "openshift-network-diagnostics", withTarget("network-check-target-service", "cluster")))
}

func (c *connectivityCheckTemplateProvider) getTemplatesForGenericPodServiceEndpointsChecks(recorder events.Recorder) []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration {
	var templates []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration
	addresses, err := c.listAddressesForGenericPodServiceEndpoints(recorder)
	if err != nil {
		recorder.Warningf("EndpointDetectionFailure", "unable to determine openshift-network-diagnostics network-check-target endpoints: %v", err)
		return nil
	}

	for _, address := range addresses {
		templates = append(templates, NewPodNetworkConnectivityCheckTemplate(net.JoinHostPort(address.hostName, address.port), "openshift-network-diagnostics", withTarget("network-check-target", strings.Split(address.nodeName, ".")[0])))
	}
	return templates
}

// listAddressesForGenericPodServiceEndpoints returns network-check-target service endpoints ip
func (c *connectivityCheckTemplateProvider) listAddressesForGenericPodServiceEndpoints(recorder events.Recorder) ([]endpointInfo, error) {
	var results []endpointInfo
	endpoints, err := c.diagnosticsEndpointsLister.Endpoints("openshift-network-diagnostics").Get("network-check-target")
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

func (c *connectivityCheckTemplateProvider) getTemplatesForAPILoadBalancerChecks(recorder events.Recorder) []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration {
	var templates []*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration
	infrastructure, err := c.infrastructureLister.Get("cluster")
	if err != nil {
		recorder.Warningf("EndpointDetectionFailure", "error detecting api load balancer endpoints: %v", err)
		return nil
	}

	apiUrl, err := url.Parse(infrastructure.Status.APIServerURL)
	if err != nil {
		recorder.Warningf("EndpointDetectionFailure", "error detecting external api load balancer endpoint: %v", err)

	} else {
		templates = append(templates, NewPodNetworkConnectivityCheckTemplate(apiUrl.Host, "openshift-network-diagnostics", withTarget("load-balancer", "api-external")))
	}

	apiInternalUrl, err := url.Parse(infrastructure.Status.APIServerInternalURL)
	if err != nil {
		recorder.Warningf("EndpointDetectionFailure", "error detecting internal api load balancer endpoint: %v", err)
	} else {
		templates = append(templates, NewPodNetworkConnectivityCheckTemplate(apiInternalUrl.Host, "openshift-network-diagnostics", withTarget("load-balancer", "api-internal")))
	}
	return templates
}

type endpointInfo struct {
	hostName string
	port     string
	nodeName string
}

func withTarget(label, target string) func(check *applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration) {
	return WithTarget(label + "-" + target)
}

func Start(ctx context.Context, kubeConfig *rest.Config) error {
	protoKubeConfig := rest.CopyConfig(kubeConfig)
	protoKubeConfig.ContentType = "application/vnd.kubernetes.protobuf,application/json"
	eventRecorder := &eventrecorder.LoggingRecorder{}
	kubeClient, err := kubernetes.NewForConfig(protoKubeConfig)
	if err != nil {
		return err
	}
	configClient, err := configv1client.NewForConfig(kubeConfig)
	if err != nil {
		return err
	}
	operatorClient, dynamicInformers, err := genericoperatorclient.NewClusterScopedOperatorClient(
		clock.RealClock{},
		kubeConfig,
		operatorv1.GroupVersion.WithResource("openshiftapiservers"),
		operatorv1.GroupVersion.WithKind("OpenShiftAPIServer"),
		nil,
		nil)
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
		"openshift-network-diagnostics",
		"openshift-kube-apiserver",
		"openshift-apiserver",
		"default",
		"",
	)

	configInformers := configinformers.NewSharedInformerFactory(configClient, 10*time.Minute)
	operatorcontrolplaneInformers := operatorcontrolplaneinformers.NewSharedInformerFactoryWithOptions(operatorcontrolplaneClient,
		10*time.Minute, operatorcontrolplaneinformers.WithNamespace("openshift-network-diagnostics"))
	connectivityCheckController := NewNetworkConnectivityCheckController(
		operatorClient,
		configClient,
		operatorcontrolplaneClient,
		apiextensionsClient,
		kubeInformersForNamespaces,
		configInformers,
		apiextensionsInformers,
		operatorcontrolplaneInformers,
		eventRecorder,
	)

	go connectivityCheckController.Run(ctx, 1)
	apiextensionsInformers.Start(ctx.Done())
	kubeInformersForNamespaces.Start(ctx.Done())
	dynamicInformers.Start(ctx.Done())
	configInformers.Start(ctx.Done())
	operatorcontrolplaneInformers.Start(ctx.Done())

	return nil
}
