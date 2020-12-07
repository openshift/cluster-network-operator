module github.com/openshift/cluster-network-operator

go 1.15

require (
	github.com/Masterminds/goutils v1.1.0 // indirect
	github.com/Masterminds/semver v1.5.0
	github.com/Masterminds/sprig v2.22.0+incompatible
	github.com/containernetworking/cni v0.8.0
	github.com/ghodss/yaml v1.0.1-0.20190212211648-25d852aebe32
	github.com/go-bindata/go-bindata v3.1.2+incompatible
	github.com/go-logr/logr v0.3.0 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/gophercloud/gophercloud v0.10.0
	github.com/gophercloud/utils v0.0.0-20191020172814-bd86af96d544
	github.com/huandu/xstrings v1.3.2 // indirect
	github.com/mitchellh/copystructure v1.0.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.1 // indirect
	github.com/onsi/gomega v1.10.1
	github.com/openshift/api v0.0.0-20201201210054-c6debb38648f
	github.com/openshift/build-machinery-go v0.0.0-20200917070002-f171684f77ab
	github.com/openshift/client-go v0.0.0-20201020074620-f8fd44879f7c
	github.com/openshift/library-go v0.0.0-20201130154959-bd449d1e2e25
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.7.1
	github.com/spf13/cobra v1.0.0
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.4.0
	github.com/vishvananda/netlink v1.1.0
	golang.org/x/net v0.0.0-20201202161906-c7110b5ffcbb // indirect
	golang.org/x/text v0.3.4 // indirect
	gopkg.in/yaml.v2 v2.4.0
	k8s.io/api v0.19.4
	k8s.io/apiextensions-apiserver v0.19.4
	k8s.io/apimachinery v0.19.4
	k8s.io/client-go v0.19.4
	k8s.io/code-generator v0.19.4
	k8s.io/component-base v0.19.4
	k8s.io/klog/v2 v2.4.0
	k8s.io/kube-proxy v0.19.4
	k8s.io/kubernetes v1.19.4 // indirect
	k8s.io/utils v0.0.0-20200729134348-d5654de09c73
	sigs.k8s.io/controller-runtime v0.6.3
	sigs.k8s.io/structured-merge-diff/v4 v4.0.2 // indirect
)

replace (
	github.com/onsi/ginkgo => github.com/onsi/ginkgo v1.11.0
	k8s.io/api => github.com/openshift/kubernetes/staging/src/k8s.io/api v0.0.0-20201105175657-1bb012236ace
	k8s.io/apiextensions-apiserver => github.com/openshift/kubernetes/staging/src/k8s.io/apiextensions-apiserver v0.0.0-20201105175657-1bb012236ace
	k8s.io/apimachinery => github.com/openshift/kubernetes/staging/src/k8s.io/apimachinery v0.0.0-20201105175657-1bb012236ace
	k8s.io/apiserver => github.com/openshift/kubernetes/staging/src/k8s.io/apiserver v0.0.0-20201105175657-1bb012236ace
	k8s.io/cli-runtime => github.com/openshift/kubernetes/staging/src/k8s.io/cli-runtime v0.0.0-20201105175657-1bb012236ace
	k8s.io/client-go => github.com/openshift/kubernetes/staging/src/k8s.io/client-go v0.0.0-20201105175657-1bb012236ace
	k8s.io/cloud-provider => github.com/openshift/kubernetes/staging/src/k8s.io/cloud-provider v0.0.0-20201105175657-1bb012236ace
	k8s.io/cluster-bootstrap => github.com/openshift/kubernetes/staging/src/k8s.io/cluster-bootstrap v0.0.0-20201105175657-1bb012236ace
	k8s.io/code-generator => github.com/openshift/kubernetes/staging/src/k8s.io/code-generator v0.0.0-20201105175657-1bb012236ace
	k8s.io/component-base => github.com/openshift/kubernetes/staging/src/k8s.io/component-base v0.0.0-20201105175657-1bb012236ace
	k8s.io/cri-api => github.com/openshift/kubernetes/staging/src/k8s.io/cri-api v0.0.0-20201105175657-1bb012236ace
	k8s.io/csi-translation-lib => github.com/openshift/kubernetes/staging/src/k8s.io/csi-translation-lib v0.0.0-20201105175657-1bb012236ace
	k8s.io/kube-aggregator => github.com/openshift/kubernetes/staging/src/k8s.io/kube-aggregator v0.0.0-20201105175657-1bb012236ace
	k8s.io/kube-controller-manager => github.com/openshift/kubernetes/staging/src/k8s.io/kube-controller-manager v0.0.0-20201105175657-1bb012236ace
	k8s.io/kube-proxy => github.com/openshift/kubernetes/staging/src/k8s.io/kube-proxy v0.0.0-20201105175657-1bb012236ace
	k8s.io/kube-scheduler => github.com/openshift/kubernetes/staging/src/k8s.io/kube-scheduler v0.0.0-20201105175657-1bb012236ace
	k8s.io/kubectl => github.com/openshift/kubernetes/staging/src/k8s.io/kubectl v0.0.0-20201105175657-1bb012236ace
	k8s.io/kubelet => github.com/openshift/kubernetes/staging/src/k8s.io/kubelet v0.0.0-20201105175657-1bb012236ace
	k8s.io/kubernetes => github.com/openshift/kubernetes v0.0.0-20201105175657-1bb012236ace
	k8s.io/legacy-cloud-providers => github.com/openshift/kubernetes/staging/src/k8s.io/legacy-cloud-providers v0.0.0-20201105175657-1bb012236ace
	k8s.io/metrics => github.com/openshift/kubernetes/staging/src/k8s.io/metrics v0.0.0-20201105175657-1bb012236ace
	k8s.io/sample-apiserver => github.com/openshift/kubernetes/staging/src/k8s.io/sample-apiserver v0.0.0-20201105175657-1bb012236ace
	k8s.io/sample-cli-plugin => github.com/openshift/kubernetes/staging/src/k8s.io/sample-cli-plugin v0.0.0-20201105175657-1bb012236ace
	k8s.io/sample-controller => github.com/openshift/kubernetes/staging/src/k8s.io/sample-controller v0.0.0-20201105175657-1bb012236ace
)
