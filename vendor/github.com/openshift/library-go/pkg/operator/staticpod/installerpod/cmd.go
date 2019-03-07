package installerpod

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strings"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/golang/glog"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	utilwait "k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/openshift/library-go/pkg/config/client"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
)

type InstallOptions struct {
	// TODO replace with genericclioptions
	KubeConfig string
	KubeClient kubernetes.Interface

	Revision  string
	Namespace string

	PodConfigMapNamePrefix        string
	SecretNamePrefixes            []string
	OptionalSecretNamePrefixes    []string
	ConfigMapNamePrefixes         []string
	OptionalConfigMapNamePrefixes []string

	ResourceDir    string
	PodManifestDir string

	Timeout time.Duration

	PodMutationFns []PodMutationFunc
}

// PodMutationFunc is a function that has a chance at changing the pod before it is created
type PodMutationFunc func(pod *corev1.Pod) error

func NewInstallOptions() *InstallOptions {
	return &InstallOptions{}
}

func (o *InstallOptions) WithPodMutationFn(podMutationFn PodMutationFunc) *InstallOptions {
	o.PodMutationFns = append(o.PodMutationFns, podMutationFn)
	return o
}

func NewInstaller() *cobra.Command {
	o := NewInstallOptions()

	cmd := &cobra.Command{
		Use:   "installer",
		Short: "Install static pod and related resources",
		Run: func(cmd *cobra.Command, args []string) {
			glog.V(1).Info(cmd.Flags())
			glog.V(1).Info(spew.Sdump(o))

			if err := o.Complete(); err != nil {
				glog.Fatal(err)
			}
			if err := o.Validate(); err != nil {
				glog.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.TODO(), time.Duration(o.Timeout)*time.Second)
			defer cancel()
			if err := o.Run(ctx); err != nil {
				glog.Fatal(err)
			}
		},
	}

	o.AddFlags(cmd.Flags())

	return cmd
}

func (o *InstallOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.KubeConfig, "kubeconfig", o.KubeConfig, "kubeconfig file or empty")
	fs.StringVar(&o.Revision, "revision", o.Revision, "identifier for this particular installation instance.  For example, a counter or a hash")
	fs.StringVar(&o.Namespace, "namespace", o.Namespace, "namespace to retrieve all resources from and create the static pod in")
	fs.StringVar(&o.PodConfigMapNamePrefix, "pod", o.PodConfigMapNamePrefix, "name of configmap that contains the pod to be created")
	fs.StringSliceVar(&o.SecretNamePrefixes, "secrets", o.SecretNamePrefixes, "list of secret names to be included")
	fs.StringSliceVar(&o.ConfigMapNamePrefixes, "configmaps", o.ConfigMapNamePrefixes, "list of configmaps to be included")
	fs.StringSliceVar(&o.OptionalSecretNamePrefixes, "optional-secrets", o.OptionalSecretNamePrefixes, "list of optional secret names to be included")
	fs.StringSliceVar(&o.OptionalConfigMapNamePrefixes, "optional-configmaps", o.OptionalConfigMapNamePrefixes, "list of optional configmaps to be included")
	fs.StringVar(&o.ResourceDir, "resource-dir", o.ResourceDir, "directory for all files supporting the static pod manifest")
	fs.StringVar(&o.PodManifestDir, "pod-manifest-dir", o.PodManifestDir, "directory for the static pod manifest")
	fs.DurationVar(&o.Timeout, "timeout-duration", 120*time.Second, "maximum time in seconds to wait for the copying to complete (default: 2m)")
}

func (o *InstallOptions) Complete() error {
	clientConfig, err := client.GetKubeConfigOrInClusterConfig(o.KubeConfig, nil)
	if err != nil {
		return err
	}

	// Use protobuf to fetch configmaps and secrets and create pods.
	protoConfig := rest.CopyConfig(clientConfig)
	protoConfig.AcceptContentTypes = "application/vnd.kubernetes.protobuf,application/json"
	protoConfig.ContentType = "application/vnd.kubernetes.protobuf"

	o.KubeClient, err = kubernetes.NewForConfig(protoConfig)
	if err != nil {
		return err
	}
	return nil
}

func (o *InstallOptions) Validate() error {
	if len(o.Revision) == 0 {
		return fmt.Errorf("--revision is required")
	}
	if len(o.Namespace) == 0 {
		return fmt.Errorf("--namespace is required")
	}
	if len(o.PodConfigMapNamePrefix) == 0 {
		return fmt.Errorf("--pod is required")
	}
	if len(o.ConfigMapNamePrefixes) == 0 {
		return fmt.Errorf("--configmaps is required")
	}
	if o.Timeout == 0 {
		return fmt.Errorf("--timeout-duration cannot be 0")
	}

	if o.KubeClient == nil {
		return fmt.Errorf("missing client")
	}

	return nil
}

func (o *InstallOptions) nameFor(prefix string) string {
	return fmt.Sprintf("%s-%s", prefix, o.Revision)
}

func (o *InstallOptions) prefixFor(name string) string {
	return name[0 : len(name)-len(fmt.Sprintf("-%s", o.Revision))]
}

func (o *InstallOptions) copyContent(ctx context.Context) error {
	secretPrefixes := sets.NewString(o.SecretNamePrefixes...)
	optionalSecretPrefixes := sets.NewString(o.OptionalSecretNamePrefixes...)
	configPrefixes := sets.NewString(o.ConfigMapNamePrefixes...)
	optionalConfigPrefixes := sets.NewString(o.OptionalConfigMapNamePrefixes...)

	// Gather secrets. If we get API server error, retry getting until we hit the timeout.
	// Retrying will prevent temporary API server blips or networking issues.
	// We return when all "required" secrets are gathered, optional secrets are not checked.
	secrets := []*corev1.Secret{}
	for _, prefix := range append(secretPrefixes.List(), optionalSecretPrefixes.List()...) {
		secret, err := o.getSecretWithRetry(ctx, prefix, optionalSecretPrefixes.Has(prefix))
		if err != nil {
			return err
		}
		// secret is nil means the secret was optional and we failed to get it.
		if secret != nil {
			secrets = append(secrets, secret)
		}
	}

	configs := []*corev1.ConfigMap{}
	for _, prefix := range append(configPrefixes.List(), optionalConfigPrefixes.List()...) {
		config, err := o.getConfigMapWithRetry(ctx, prefix, optionalConfigPrefixes.Has(prefix))
		if err != nil {
			return err
		}
		// config is nil means the config was optional and we failed to get it.
		if config != nil {
			configs = append(configs, config)
		}
	}

	// Gather pod yaml from config map
	var podContent string
	err := utilwait.PollImmediateUntil(200*time.Millisecond, func() (bool, error) {
		glog.Infof("Getting pod configmaps/%s -n %s", o.nameFor(o.PodConfigMapNamePrefix), o.Namespace)
		podConfigMap, err := o.KubeClient.CoreV1().ConfigMaps(o.Namespace).Get(o.nameFor(o.PodConfigMapNamePrefix), metav1.GetOptions{})
		switch {
		case errors.IsNotFound(err):
			return false, err
		case err != nil:
			glog.Infof("Failed to get pod %s/%s: %v", o.Namespace, o.nameFor(o.PodConfigMapNamePrefix), err)
			return false, nil
		default:
			podData, exists := podConfigMap.Data["pod.yaml"]
			if !exists {
				return false, fmt.Errorf("required 'pod.yaml' key does not exist in configmap")
			}
			podContent = strings.Replace(podData, "REVISION", o.Revision, -1)
			return true, nil
		}
	}, ctx.Done())
	if err != nil {
		return err
	}

	// Write secrets, config maps and pod to disk
	// This does not need timeout, instead we should fail hard when we are not able to write.
	resourceDir := path.Join(o.ResourceDir, o.nameFor(o.PodConfigMapNamePrefix))
	glog.Infof("Creating target resource directory %q ...", resourceDir)
	if err := os.MkdirAll(resourceDir, 0755); err != nil {
		return err
	}

	for _, secret := range secrets {
		contentDir := path.Join(resourceDir, "secrets", o.prefixFor(secret.Name))
		glog.Infof("Creating directory %q ...", contentDir)
		if err := os.MkdirAll(contentDir, 0755); err != nil {
			return err
		}
		for filename, content := range secret.Data {
			// TODO fix permissions
			glog.Infof("Writing secret manifest %q ...", path.Join(contentDir, filename))
			if err := ioutil.WriteFile(path.Join(contentDir, filename), content, 0644); err != nil {
				return err
			}
		}
	}
	for _, configmap := range configs {
		contentDir := path.Join(resourceDir, "configmaps", o.prefixFor(configmap.Name))
		glog.Infof("Creating directory %q ...", contentDir)
		if err := os.MkdirAll(contentDir, 0755); err != nil {
			return err
		}
		for filename, content := range configmap.Data {
			glog.Infof("Writing config file %q ...", path.Join(contentDir, filename))
			if err := ioutil.WriteFile(path.Join(contentDir, filename), []byte(content), 0644); err != nil {
				return err
			}
		}
	}

	podFileName := o.PodConfigMapNamePrefix + ".yaml"
	glog.Infof("Writing pod manifest %q ...", path.Join(resourceDir, podFileName))
	if err := ioutil.WriteFile(path.Join(resourceDir, podFileName), []byte(podContent), 0644); err != nil {
		return err
	}

	// copy static pod
	glog.Infof("Creating directory for static pod manifest %q ...", o.PodManifestDir)
	if err := os.MkdirAll(o.PodManifestDir, 0755); err != nil {
		return err
	}

	for _, fn := range o.PodMutationFns {
		glog.V(2).Infof("Customizing static pod ...")
		pod := resourceread.ReadPodV1OrDie([]byte(podContent))
		if err := fn(pod); err != nil {
			return err
		}
		podContent = resourceread.WritePodV1OrDie(pod)
	}

	glog.Infof("Writing static pod manifest %q ...\n%s", path.Join(o.PodManifestDir, podFileName), podContent)
	if err := ioutil.WriteFile(path.Join(o.PodManifestDir, podFileName), []byte(podContent), 0644); err != nil {
		return err
	}

	return nil
}

func (o *InstallOptions) Run(ctx context.Context) error {
	var eventTarget *corev1.ObjectReference

	// Poll for the pod here to prevent flakes when the API server is temporary down or connection refused errors.
	if pollErr := utilwait.PollImmediateUntil(200*time.Millisecond, func() (bool, error) {
		var err error
		eventTarget, err = events.GetControllerReferenceForCurrentPod(o.KubeClient, o.Namespace, nil)
		switch {
		case errors.IsNotFound(err):
			return true, err
		case err != nil:
			glog.Warningf("Failed to obtain installer pod self-reference: %v (will retry)", err)
			return false, nil
		default:
			return true, nil
		}
	}, ctx.Done()); pollErr != nil {
		return pollErr
	}

	recorder := events.NewRecorder(o.KubeClient.CoreV1().Events(o.Namespace), "static-pod-installer", eventTarget)
	if err := o.copyContent(ctx); err != nil {
		recorder.Warningf("StaticPodInstallerFailed", "Installing revision %s: %v", o.Revision, err)
		return fmt.Errorf("failed to copy: %v", err)
	}

	recorder.Eventf("StaticPodInstallerCompleted", "Successfully installed revision %s", o.Revision)
	return nil
}
