package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	buildv1 "github.com/openshift/api/build/v1"
	configv1 "github.com/openshift/api/config/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type OpenShiftAPIServerConfig struct {
	metav1.TypeMeta `json:",inline"`

	// provides the standard apiserver configuration
	configv1.GenericAPIServerConfig `json:",inline" protobuf:"bytes,1,opt,name=genericAPIServerConfig"`

	// imagePolicyConfig feeds the image policy admission plugin
	ImagePolicyConfig ImagePolicyConfig `json:"imagePolicyConfig" protobuf:"bytes,9,opt,name=imagePolicyConfig"`

	// projectConfig feeds an admission plugin
	ProjectConfig ProjectConfig `json:"projectConfig" protobuf:"bytes,10,opt,name=projectConfig"`

	// routingConfig holds information about routing and route generation
	RoutingConfig RoutingConfig `json:"routingConfig" protobuf:"bytes,11,opt,name=routingConfig"`

	// serviceAccountOAuthGrantMethod is used for determining client authorization for service account oauth client.
	// It must be either: deny, prompt, or ""
	ServiceAccountOAuthGrantMethod GrantHandlerType `json:"serviceAccountOAuthGrantMethod" protobuf:"bytes,12,opt,name=serviceAccountOAuthGrantMethod,casttype=GrantHandlerType"`

	// jenkinsPipelineConfig holds information about the default Jenkins template
	// used for JenkinsPipeline build strategy.
	// TODO this needs to become a normal plugin config
	JenkinsPipelineConfig JenkinsPipelineConfig `json:"jenkinsPipelineConfig" protobuf:"bytes,13,opt,name=jenkinsPipelineConfig"`

	// cloudProviderFile points to the cloud config file
	// TODO this needs to become a normal plugin config
	CloudProviderFile string `json:"cloudProviderFile" protobuf:"bytes,14,opt,name=cloudProviderFile"`

	// TODO this needs to be removed.
	APIServerArguments map[string][]string `json:"apiServerArguments" protobuf:"bytes,14,rep,name=apiServerArguments"`
}

type GrantHandlerType string

const (
	// GrantHandlerAuto auto-approves client authorization grant requests
	GrantHandlerAuto GrantHandlerType = "auto"
	// GrantHandlerPrompt prompts the user to approve new client authorization grant requests
	GrantHandlerPrompt GrantHandlerType = "prompt"
	// GrantHandlerDeny auto-denies client authorization grant requests
	GrantHandlerDeny GrantHandlerType = "deny"
)

// RoutingConfig holds the necessary configuration options for routing to subdomains
type RoutingConfig struct {
	// subdomain is the suffix appended to $service.$namespace. to form the default route hostname
	// DEPRECATED: This field is being replaced by routers setting their own defaults. This is the
	// "default" route.
	Subdomain string `json:"subdomain" protobuf:"bytes,1,opt,name=subdomain"`
}

type ImagePolicyConfig struct {
	// maxImagesBulkImportedPerRepository controls the number of images that are imported when a user
	// does a bulk import of a Docker repository. This number is set low to prevent users from
	// importing large numbers of images accidentally. Set -1 for no limit.
	MaxImagesBulkImportedPerRepository int `json:"maxImagesBulkImportedPerRepository" protobuf:"varint,1,opt,name=maxImagesBulkImportedPerRepository"`
	// allowedRegistriesForImport limits the docker registries that normal users may import
	// images from. Set this list to the registries that you trust to contain valid Docker
	// images and that you want applications to be able to import from. Users with
	// permission to create Images or ImageStreamMappings via the API are not affected by
	// this policy - typically only administrators or system integrations will have those
	// permissions.
	AllowedRegistriesForImport AllowedRegistries `json:"allowedRegistriesForImport" protobuf:"bytes,2,rep,name=allowedRegistriesForImport"`

	// internalRegistryHostname sets the hostname for the default internal image
	// registry. The value must be in "hostname[:port]" format.
	// For backward compatibility, users can still use OPENSHIFT_DEFAULT_REGISTRY
	// environment variable but this setting overrides the environment variable.
	InternalRegistryHostname string `json:"internalRegistryHostname" protobuf:"bytes,3,opt,name=internalRegistryHostname"`
	// externalRegistryHostname sets the hostname for the default external image
	// registry. The external hostname should be set only when the image registry
	// is exposed externally. The value is used in 'publicDockerImageRepository'
	// field in ImageStreams. The value must be in "hostname[:port]" format.
	ExternalRegistryHostname string `json:"externalRegistryHostname" protobuf:"bytes,4,opt,name=externalRegistryHostname"`

	// additionalTrustedCA is a path to a pem bundle file containing additional CAs that
	// should be trusted during imagestream import.
	AdditionalTrustedCA string `json:"additionalTrustedCA" protobuf:"bytes,5,opt,name=additionalTrustedCA"`
}

// AllowedRegistries represents a list of registries allowed for the image import.
type AllowedRegistries []RegistryLocation

// RegistryLocation contains a location of the registry specified by the registry domain
// name. The domain name might include wildcards, like '*' or '??'.
type RegistryLocation struct {
	// DomainName specifies a domain name for the registry
	// In case the registry use non-standard (80 or 443) port, the port should be included
	// in the domain name as well.
	DomainName string `json:"domainName" protobuf:"bytes,1,opt,name=domainName"`
	// Insecure indicates whether the registry is secure (https) or insecure (http)
	// By default (if not specified) the registry is assumed as secure.
	Insecure bool `json:"insecure,omitempty" protobuf:"varint,2,opt,name=insecure"`
}

type ProjectConfig struct {
	// defaultNodeSelector holds default project node label selector
	DefaultNodeSelector string `json:"defaultNodeSelector" protobuf:"bytes,1,opt,name=defaultNodeSelector"`

	// projectRequestMessage is the string presented to a user if they are unable to request a project via the projectrequest api endpoint
	ProjectRequestMessage string `json:"projectRequestMessage" protobuf:"bytes,2,opt,name=projectRequestMessage"`

	// projectRequestTemplate is the template to use for creating projects in response to projectrequest.
	// It is in the format namespace/template and it is optional.
	// If it is not specified, a default template is used.
	ProjectRequestTemplate string `json:"projectRequestTemplate" protobuf:"bytes,3,opt,name=projectRequestTemplate"`
}

// JenkinsPipelineConfig holds configuration for the Jenkins pipeline strategy
type JenkinsPipelineConfig struct {
	// autoProvisionEnabled determines whether a Jenkins server will be spawned from the provided
	// template when the first build config in the project with type JenkinsPipeline
	// is created. When not specified this option defaults to true.
	AutoProvisionEnabled *bool `json:"autoProvisionEnabled" protobuf:"varint,1,opt,name=autoProvisionEnabled"`
	// templateNamespace contains the namespace name where the Jenkins template is stored
	TemplateNamespace string `json:"templateNamespace" protobuf:"bytes,2,opt,name=templateNamespace"`
	// templateName is the name of the default Jenkins template
	TemplateName string `json:"templateName" protobuf:"bytes,3,opt,name=templateName"`
	// serviceName is the name of the Jenkins service OpenShift uses to detect
	// whether a Jenkins pipeline handler has already been installed in a project.
	// This value *must* match a service name in the provided template.
	ServiceName string `json:"serviceName" protobuf:"bytes,4,opt,name=serviceName"`
	// parameters specifies a set of optional parameters to the Jenkins template.
	Parameters map[string]string `json:"parameters" protobuf:"bytes,5,rep,name=parameters"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type OpenShiftControllerManagerConfig struct {
	metav1.TypeMeta `json:",inline"`

	KubeClientConfig configv1.KubeClientConfig `json:"kubeClientConfig"`

	// servingInfo describes how to start serving
	ServingInfo *configv1.HTTPServingInfo `json:"servingInfo"`

	// leaderElection defines the configuration for electing a controller instance to make changes to
	// the cluster. If unspecified, the ControllerTTL value is checked to determine whether the
	// legacy direct etcd election code will be used.
	LeaderElection configv1.LeaderElection `json:"leaderElection"`

	// controllers is a list of controllers to enable.  '*' enables all on-by-default controllers, 'foo' enables the controller "+
	// named 'foo', '-foo' disables the controller named 'foo'.
	// Defaults to "*".
	Controllers []string `json:"controllers"`

	ResourceQuota      ResourceQuotaControllerConfig    `json:"resourceQuota"`
	ServiceServingCert ServiceServingCert               `json:"serviceServingCert"`
	Deployer           DeployerControllerConfig         `json:"deployer"`
	Build              BuildControllerConfig            `json:"build"`
	ServiceAccount     ServiceAccountControllerConfig   `json:"serviceAccount"`
	DockerPullSecret   DockerPullSecretControllerConfig `json:"dockerPullSecret"`
	Network            NetworkControllerConfig          `json:"network"`
	Ingress            IngressControllerConfig          `json:"ingress"`
	ImageImport        ImageImportControllerConfig      `json:"imageImport"`
	SecurityAllocator  SecurityAllocator                `json:"securityAllocator"`
}

type DeployerControllerConfig struct {
	ImageTemplateFormat ImageConfig `json:"imageTemplateFormat"`
}

type BuildControllerConfig struct {
	ImageTemplateFormat ImageConfig `json:"imageTemplateFormat"`

	BuildDefaults  *BuildDefaultsConfig  `json:"buildDefaults"`
	BuildOverrides *BuildOverridesConfig `json:"buildOverrides"`
}

type ResourceQuotaControllerConfig struct {
	ConcurrentSyncs int32           `json:"concurrentSyncs"`
	SyncPeriod      metav1.Duration `json:"syncPeriod"`
	MinResyncPeriod metav1.Duration `json:"minResyncPeriod"`
}

type IngressControllerConfig struct {
	// ingressIPNetworkCIDR controls the range to assign ingress ips from for services of type LoadBalancer on bare
	// metal. If empty, ingress ips will not be assigned. It may contain a single CIDR that will be allocated from.
	// For security reasons, you should ensure that this range does not overlap with the CIDRs reserved for external ips,
	// nodes, pods, or services.
	IngressIPNetworkCIDR string `json:"ingressIPNetworkCIDR"`
}

// MasterNetworkConfig to be passed to the compiled in network plugin
type NetworkControllerConfig struct {
	NetworkPluginName string `json:"networkPluginName"`
	// clusterNetworks contains a list of cluster networks that defines the global overlay networks L3 space.
	ClusterNetworks    []ClusterNetworkEntry `json:"clusterNetworks"`
	ServiceNetworkCIDR string                `json:"serviceNetworkCIDR"`
	VXLANPort          uint32                `json:"vxLANPort"`
}

type ServiceAccountControllerConfig struct {
	// managedNames is a list of service account names that will be auto-created in every namespace.
	// If no names are specified, the ServiceAccountsController will not be started.
	ManagedNames []string `json:"managedNames"`
}

type DockerPullSecretControllerConfig struct {
	// registryURLs is a list of urls that the docker pull secrets should be valid for.
	RegistryURLs []string `json:"registryURLs"`
}

type ImageImportControllerConfig struct {
	// maxScheduledImageImportsPerMinute is the maximum number of image streams that will be imported in the background per minute.
	// The default value is 60. Set to -1 for unlimited.
	MaxScheduledImageImportsPerMinute int `json:"maxScheduledImageImportsPerMinute"`
	// disableScheduledImport allows scheduled background import of images to be disabled.
	DisableScheduledImport bool `json:"disableScheduledImport"`
	// scheduledImageImportMinimumIntervalSeconds is the minimum number of seconds that can elapse between when image streams
	// scheduled for background import are checked against the upstream repository. The default value is 15 minutes.
	ScheduledImageImportMinimumIntervalSeconds int `json:"scheduledImageImportMinimumIntervalSeconds"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// BuildDefaultsConfig controls the default information for Builds
type BuildDefaultsConfig struct {
	metav1.TypeMeta `json:",inline"`

	// gitHTTPProxy is the location of the HTTPProxy for Git source
	GitHTTPProxy string `json:"gitHTTPProxy,omitempty"`

	// gitHTTPSProxy is the location of the HTTPSProxy for Git source
	GitHTTPSProxy string `json:"gitHTTPSProxy,omitempty"`

	// gitNoProxy is the list of domains for which the proxy should not be used
	GitNoProxy string `json:"gitNoProxy,omitempty"`

	// env is a set of default environment variables that will be applied to the
	// build if the specified variables do not exist on the build
	Env []corev1.EnvVar `json:"env,omitempty"`

	// sourceStrategyDefaults are default values that apply to builds using the
	// source strategy.
	SourceStrategyDefaults *SourceStrategyDefaultsConfig `json:"sourceStrategyDefaults,omitempty"`

	// imageLabels is a list of docker labels that are applied to the resulting image.
	// User can override a default label by providing a label with the same name in their
	// Build/BuildConfig.
	ImageLabels []buildv1.ImageLabel `json:"imageLabels,omitempty"`

	// nodeSelector is a selector which must be true for the build pod to fit on a node
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// annotations are annotations that will be added to the build pod
	Annotations map[string]string `json:"annotations,omitempty"`

	// resources defines resource requirements to execute the build.
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// SourceStrategyDefaultsConfig contains values that apply to builds using the
// source strategy.
type SourceStrategyDefaultsConfig struct {

	// incremental indicates if s2i build strategies should perform an incremental
	// build or not
	Incremental *bool `json:"incremental,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// BuildOverridesConfig controls override settings for builds
type BuildOverridesConfig struct {
	metav1.TypeMeta `json:",inline"`

	// forcePull indicates whether the build strategy should always be set to ForcePull=true
	ForcePull bool `json:"forcePull"`

	// imageLabels is a list of docker labels that are applied to the resulting image.
	// If user provided a label in their Build/BuildConfig with the same name as one in this
	// list, the user's label will be overwritten.
	ImageLabels []buildv1.ImageLabel `json:"imageLabels,omitempty"`

	// nodeSelector is a selector which must be true for the build pod to fit on a node
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// annotations are annotations that will be added to the build pod
	Annotations map[string]string `json:"annotations,omitempty"`

	// tolerations is a list of Tolerations that will override any existing
	// tolerations set on a build pod.
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

// ImageConfig holds the necessary configuration options for building image names for system components
type ImageConfig struct {
	// Format is the format of the name to be built for the system component
	Format string `json:"format"`
	// Latest determines if the latest tag will be pulled from the registry
	Latest bool `json:"latest"`
}

// ServiceServingCert holds configuration for service serving cert signer which creates cert/key pairs for
// pods fulfilling a service to serve with.
type ServiceServingCert struct {
	// Signer holds the signing information used to automatically sign serving certificates.
	// If this value is nil, then certs are not signed automatically.
	Signer *configv1.CertInfo `json:"signer"`
}

// ClusterNetworkEntry defines an individual cluster network. The CIDRs cannot overlap with other cluster network CIDRs, CIDRs reserved for external ips, CIDRs reserved for service networks, and CIDRs reserved for ingress ips.
type ClusterNetworkEntry struct {
	// CIDR defines the total range of a cluster networks address space.
	CIDR string `json:"cidr"`
	// HostSubnetLength is the number of bits of the accompanying CIDR address to allocate to each node. eg, 8 would mean that each node would have a /24 slice of the overlay network for its pod.
	HostSubnetLength uint32 `json:"hostSubnetLength"`
}

// SecurityAllocator controls the automatic allocation of UIDs and MCS labels to a project. If nil, allocation is disabled.
type SecurityAllocator struct {
	// UIDAllocatorRange defines the total set of Unix user IDs (UIDs) that will be allocated to projects automatically, and the size of the
	// block each namespace gets. For example, 1000-1999/10 will allocate ten UIDs per namespace, and will be able to allocate up to 100 blocks
	// before running out of space. The default is to allocate from 1 billion to 2 billion in 10k blocks (which is the expected size of the
	// ranges Docker images will use once user namespaces are started).
	UIDAllocatorRange string `json:"uidAllocatorRange"`
	// MCSAllocatorRange defines the range of MCS categories that will be assigned to namespaces. The format is
	// "<prefix>/<numberOfLabels>[,<maxCategory>]". The default is "s0/2" and will allocate from c0 -> c1023, which means a total of 535k labels
	// are available (1024 choose 2 ~ 535k). If this value is changed after startup, new projects may receive labels that are already allocated
	// to other projects. Prefix may be any valid SELinux set of terms (including user, role, and type), although leaving them as the default
	// will allow the server to set them automatically.
	//
	// Examples:
	// * s0:/2     - Allocate labels from s0:c0,c0 to s0:c511,c511
	// * s0:/2,512 - Allocate labels from s0:c0,c0,c0 to s0:c511,c511,511
	//
	MCSAllocatorRange string `json:"mcsAllocatorRange"`
	// MCSLabelsPerProject defines the number of labels that should be reserved per project. The default is 5 to match the default UID and MCS
	// ranges (100k namespaces, 535k/5 labels).
	MCSLabelsPerProject int `json:"mcsLabelsPerProject"`
}
