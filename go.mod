module github.com/openshift/cluster-network-operator

go 1.16

require (
	github.com/BurntSushi/toml v0.4.1 // indirect
	github.com/Masterminds/goutils v1.1.0 // indirect
	github.com/Masterminds/semver v1.5.0
	github.com/Masterminds/sprig v2.22.0+incompatible
	github.com/containernetworking/cni v0.8.0
	github.com/evanphx/json-patch v5.6.0+incompatible // indirect
	github.com/ghodss/yaml v1.0.1-0.20190212211648-25d852aebe32
	github.com/go-bindata/go-bindata v3.1.2+incompatible
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/google/uuid v1.2.0 // indirect
	github.com/gophercloud/gophercloud v0.14.0
	github.com/gophercloud/utils v0.0.0-20201221031838-d93cf4b3fa50
	github.com/huandu/xstrings v1.3.2 // indirect
	github.com/mitchellh/copystructure v1.0.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.1 // indirect
	github.com/onsi/gomega v1.17.0
	github.com/openshift/api v0.0.0-20211209135129-c58d9f695577
	github.com/openshift/build-machinery-go v0.0.0-20211213093930-7e33a7eb4ce3
	github.com/openshift/client-go v0.0.0-20211209144617-7385dd6338e3
	github.com/openshift/library-go v0.0.0-20211220195323-eca2c467c492
	github.com/openshift/machine-api-operator v0.2.1-0.20201203125141-79567cb3368e
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.11.0
	github.com/spf13/cobra v1.2.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.7.0
	github.com/vishvananda/netlink v1.1.0
	github.com/vishvananda/netns v0.0.0-20200728191858-db3c7e526aae // indirect
	golang.org/x/net v0.0.0-20210825183410-e898025ed96a
	gopkg.in/yaml.v2 v2.4.0
	k8s.io/api v0.23.0
	k8s.io/apiextensions-apiserver v0.23.0
	k8s.io/apimachinery v0.23.0
	k8s.io/client-go v0.23.0
	k8s.io/code-generator v0.23.0
	k8s.io/component-base v0.23.0
	k8s.io/klog/v2 v2.30.0
	k8s.io/kube-proxy v0.22.1
	k8s.io/utils v0.0.0-20210930125809-cb0fa318a74b
	sigs.k8s.io/cluster-api-provider-openstack v0.3.3
	sigs.k8s.io/controller-runtime v0.11.0
)

replace (
	sigs.k8s.io/cluster-api-provider-aws => github.com/openshift/cluster-api-provider-aws v0.2.1-0.20201125052318-b85a18cbf338
	sigs.k8s.io/cluster-api-provider-azure => github.com/openshift/cluster-api-provider-azure v0.1.0-alpha.3.0.20201130182513-88b90230f2a4
	sigs.k8s.io/cluster-api-provider-openstack => github.com/openshift/cluster-api-provider-openstack v0.0.0-20210107201226-5f60693f7a71
)
