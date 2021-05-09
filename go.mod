module github.com/openshift/cluster-network-operator

go 1.16

require (
	github.com/Masterminds/goutils v1.1.0 // indirect
	github.com/Masterminds/semver v1.5.0
	github.com/Masterminds/sprig v2.22.0+incompatible
	github.com/containernetworking/cni v0.8.0
	github.com/ghodss/yaml v1.0.1-0.20190212211648-25d852aebe32
	github.com/go-bindata/go-bindata v3.1.2+incompatible
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/gophercloud/gophercloud v0.14.0
	github.com/gophercloud/utils v0.0.0-20201221031838-d93cf4b3fa50
	github.com/huandu/xstrings v1.3.2 // indirect
	github.com/mitchellh/copystructure v1.0.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.1 // indirect
	github.com/onsi/gomega v1.10.5
	github.com/openshift/api v0.0.0-20210420151714-a3c8fa53e01b
	github.com/openshift/build-machinery-go v0.0.0-20210412141922-56d6e6c51074
	github.com/openshift/client-go v0.0.0-20201214125552-e615e336eb49
	github.com/openshift/library-go v0.0.0-20210204161529-5c46b163e958
	github.com/openshift/machine-api-operator v0.2.1-0.20201203125141-79567cb3368e
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.9.0
	github.com/spf13/cobra v1.1.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.6.1
	github.com/vishvananda/netlink v1.1.0
	github.com/vishvananda/netns v0.0.0-20200728191858-db3c7e526aae // indirect
	golang.org/x/net v0.0.0-20210420210106-798c2154c571 // indirect
	gopkg.in/yaml.v2 v2.4.0
	k8s.io/api v0.21.0
	k8s.io/apiextensions-apiserver v0.21.0
	k8s.io/apimachinery v0.21.0
	k8s.io/client-go v0.21.0
	k8s.io/code-generator v0.21.0
	k8s.io/component-base v0.21.0
	k8s.io/klog/v2 v2.8.0
	k8s.io/kube-proxy v0.21.0
	k8s.io/metrics v0.21.0 // indirect
	k8s.io/utils v0.0.0-20210111153108-fddb29f9d009
	sigs.k8s.io/cluster-api-provider-openstack v0.3.3
	sigs.k8s.io/controller-runtime v0.9.0-beta.0
	sigs.k8s.io/structured-merge-diff/v4 v4.1.1 // indirect
)

replace (
	github.com/googleapis/gnostic => github.com/googleapis/gnostic v0.4.2
	google.golang.org/grpc => google.golang.org/grpc v1.27.1
	k8s.io/apiserver => github.com/openshift/kubernetes-apiserver v0.0.0-20210419140141-620426e63a99 // points to temporary-watch-reduction-patch-1.21 to pick up k/k/pull/100959
	sigs.k8s.io/cluster-api-provider-aws => github.com/openshift/cluster-api-provider-aws v0.2.1-0.20201125052318-b85a18cbf338
	sigs.k8s.io/cluster-api-provider-azure => github.com/openshift/cluster-api-provider-azure v0.1.0-alpha.3.0.20201130182513-88b90230f2a4
	sigs.k8s.io/cluster-api-provider-openstack => github.com/openshift/cluster-api-provider-openstack v0.0.0-20210107201226-5f60693f7a71
)
