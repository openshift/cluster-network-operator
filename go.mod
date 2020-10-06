module github.com/openshift/cluster-network-operator

go 1.14

require (
	github.com/Masterminds/goutils v1.1.0 // indirect
	github.com/Masterminds/semver v1.5.0
	github.com/Masterminds/sprig v2.22.0+incompatible
	github.com/containernetworking/cni v0.7.1
	github.com/ghodss/yaml v1.0.1-0.20190212211648-25d852aebe32
	github.com/go-logr/logr v0.1.0
	github.com/gophercloud/gophercloud v0.10.0
	github.com/gophercloud/utils v0.0.0-20191020172814-bd86af96d544
	github.com/huandu/xstrings v1.3.2 // indirect
	github.com/mitchellh/copystructure v1.0.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.1 // indirect
	github.com/onsi/gomega v1.10.1
	github.com/openshift/api v0.0.0-20200701144905-de5b010b2b38
	github.com/openshift/library-go v0.0.0-20200630145007-34ebc8778b33
	github.com/pkg/errors v0.9.1
	github.com/spf13/pflag v1.0.5
	github.com/vishvananda/netlink v1.0.0
	gopkg.in/yaml.v2 v2.3.0
	k8s.io/api v0.18.6
	k8s.io/apimachinery v0.18.6
	k8s.io/client-go v12.0.0+incompatible
	k8s.io/kube-proxy v0.18.6
	k8s.io/utils v0.0.0-20200603063816-c1c6865ac451
	sigs.k8s.io/controller-runtime v0.5.2
)

replace (
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.18.6
	k8s.io/client-go => k8s.io/client-go v0.18.6
	k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.18.6
	sigs.k8s.io/controller-runtime => sigs.k8s.io/controller-runtime v0.0.0-20200623140740-add0b6444f50
)
