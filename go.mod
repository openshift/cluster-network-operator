module github.com/openshift/cluster-network-operator

go 1.13

require (
	github.com/Masterminds/semver v1.5.0
	github.com/Masterminds/sprig v2.22.0+incompatible
	github.com/containernetworking/cni v0.7.1
	github.com/fsnotify/fsnotify v1.4.8 // indirect
	github.com/ghodss/yaml v1.0.1-0.20190212211648-25d852aebe32
	github.com/gophercloud/gophercloud v0.10.0
	github.com/gophercloud/utils v0.0.0-20191020172814-bd86af96d544
	github.com/imdario/mergo v0.3.8 // indirect
	github.com/mitchellh/reflectwalk v1.0.1 // indirect
	github.com/onsi/gomega v1.8.1
	github.com/openshift/api v0.0.0-20200205133042-34f0ec8dab87
	github.com/openshift/library-go v0.0.0-20191112181215-0597a29991ca
	github.com/operator-framework/operator-sdk v0.17.0
	github.com/pkg/errors v0.9.1
	github.com/spf13/pflag v1.0.5
	github.com/vishvananda/netlink v1.0.0
	github.com/vishvananda/netns v0.0.0-20191106174202-0a2b9b5464df // indirect
	gopkg.in/yaml.v2 v2.2.8
	k8s.io/api v0.17.4
	k8s.io/apimachinery v0.17.4
	k8s.io/client-go v12.0.0+incompatible
	k8s.io/kube-proxy v0.0.0-20190918162534-de037b596c1e
	sigs.k8s.io/controller-runtime v0.5.2
)

replace k8s.io/client-go => k8s.io/client-go v0.17.4
