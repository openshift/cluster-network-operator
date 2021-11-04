module github.com/openshift/cluster-network-operator

go 1.16

require (
	github.com/BurntSushi/toml v0.4.1 // indirect
	github.com/Masterminds/goutils v1.1.0 // indirect
	github.com/Masterminds/semver v1.5.0
	github.com/Masterminds/sprig v2.22.0+incompatible
	github.com/containernetworking/cni v1.0.1
	github.com/containernetworking/plugins v1.0.1
	github.com/fsnotify/fsnotify v1.4.9
	github.com/ghodss/yaml v1.0.1-0.20190212211648-25d852aebe32
	github.com/go-bindata/go-bindata v3.1.2+incompatible
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/gophercloud/gophercloud v0.14.0
	github.com/gophercloud/utils v0.0.0-20201221031838-d93cf4b3fa50
	github.com/huandu/xstrings v1.3.2 // indirect
	github.com/mitchellh/copystructure v1.0.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.1 // indirect
	github.com/onsi/gomega v1.15.0
	github.com/opencontainers/runtime-spec v1.0.3-0.20200929063507-e6143ca7d51d
	github.com/openshift/api v0.0.0-20210817132244-67c28690af52
	github.com/openshift/build-machinery-go v0.0.0-20210712174854-1bb7fd1518d3
	github.com/openshift/client-go v0.0.0-20210521082421-73d9475a9142
	github.com/openshift/library-go v0.0.0-20210708173104-7e7d216ed91c
	github.com/openshift/machine-api-operator v0.2.1-0.20201203125141-79567cb3368e
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.11.0
	github.com/spf13/cobra v1.2.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.7.0
	github.com/vishvananda/netlink v1.1.1-0.20210330154013-f5de75959ad5
	golang.org/x/crypto v0.0.0-20210513164829-c07d793c2f9a // indirect
	golang.org/x/net v0.0.0-20210520170846-37e1c6afe023
	golang.org/x/tools v0.1.5 // indirect
	google.golang.org/grpc v1.38.0
	gopkg.in/yaml.v2 v2.4.0
	k8s.io/api v0.22.1
	k8s.io/apiextensions-apiserver v0.22.1
	k8s.io/apimachinery v0.22.1
	k8s.io/client-go v0.22.1
	k8s.io/code-generator v0.22.1
	k8s.io/component-base v0.22.1
	k8s.io/cri-api v0.22.3
	k8s.io/klog/v2 v2.9.0
	k8s.io/kube-proxy v0.22.1
	k8s.io/utils v0.0.0-20210820185131-d34e5cb4466e
	sigs.k8s.io/cluster-api-provider-openstack v0.3.3
	sigs.k8s.io/controller-runtime v0.9.0-beta.0
)

replace (
	sigs.k8s.io/cluster-api-provider-aws => github.com/openshift/cluster-api-provider-aws v0.2.1-0.20201125052318-b85a18cbf338
	sigs.k8s.io/cluster-api-provider-azure => github.com/openshift/cluster-api-provider-azure v0.1.0-alpha.3.0.20201130182513-88b90230f2a4
	sigs.k8s.io/cluster-api-provider-openstack => github.com/openshift/cluster-api-provider-openstack v0.0.0-20210107201226-5f60693f7a71
)
