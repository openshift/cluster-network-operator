package checkendpoints

import (
	"context"
	"os"
	"time"

	operatorcontrolplaneclient "github.com/openshift/client-go/operatorcontrolplane/clientset/versioned"
	operatorcontrolplaneinformers "github.com/openshift/client-go/operatorcontrolplane/informers/externalversions"
	"github.com/openshift/cluster-network-operator/pkg/cmd/checkendpoints/controller"
	"github.com/openshift/cluster-network-operator/pkg/version"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/retry"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
)

func NewCheckEndpointsCommand() *cobra.Command {
	config := controllercmd.NewControllerCommandConfig("check-endpoints", version.Get(), func(ctx context.Context, cctx *controllercmd.ControllerContext) error {
		podName := os.Getenv("POD_NAME")
		namespace := os.Getenv("POD_NAMESPACE")
		kubeClient := kubernetes.NewForConfigOrDie(cctx.ProtoKubeConfig)
		apiextensionsClient := apiextensionsclient.NewForConfigOrDie(cctx.KubeConfig)
		operatorcontrolplaneClient := operatorcontrolplaneclient.NewForConfigOrDie(cctx.KubeConfig)
		kubeInformers := informers.NewSharedInformerFactoryWithOptions(kubeClient, 10*time.Minute, informers.WithNamespace(namespace))
		operatorcontrolplaneInformers := operatorcontrolplaneinformers.NewSharedInformerFactoryWithOptions(operatorcontrolplaneClient, 10*time.Minute, operatorcontrolplaneinformers.WithNamespace(namespace))
		apiextensionsInformers := apiextensionsinformers.NewSharedInformerFactory(apiextensionsClient, 10*time.Minute)

		// create a recorder that sets the pod node as the involved object in events
		var involvedObjectRef *corev1.ObjectReference
		err := retry.RetryOnConnectionErrors(ctx, func(context.Context) (bool, error) {
			pod, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			node, err := kubeClient.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			involvedObjectRef = &corev1.ObjectReference{
				Kind:       "Node",
				Namespace:  namespace,
				Name:       node.Name,
				UID:        node.UID,
				APIVersion: node.APIVersion,
			}
			return true, nil
		})
		if err != nil {
			return err
		}
		recorder := events.NewRecorder(kubeClient.CoreV1().Events(namespace), "check-endpoint", involvedObjectRef)

		check := controller.NewPodNetworkConnectivityCheckController(
			podName,
			namespace,
			operatorcontrolplaneClient.ControlplaneV1alpha1(),
			operatorcontrolplaneInformers.Controlplane().V1alpha1().PodNetworkConnectivityChecks(),
			kubeInformers.Core().V1().Secrets(),
			recorder,
		)

		timeToStart := newTimeToStartController(
			apiextensionsInformers.Apiextensions().V1().CustomResourceDefinitions(),
			recorder,
		)

		stopController := newStopController(
			apiextensionsInformers.Apiextensions().V1().CustomResourceDefinitions(),
			recorder,
		)

		controller.RegisterMetrics()

		// block until the PodNetworkConnectivityCheck CRD exists
		apiextensionsInformers.Start(ctx.Done())
		ttsContext, ttsCancel := context.WithCancel(ctx)
		go timeToStart.Run(ttsContext, 1)
		select {
		case err := <-timeToStart.Ready():
			ttsCancel()
			if err != nil {
				return err
			}
		case <-ctx.Done():
			ttsCancel()
			return nil
		}

		// continue startup
		operatorcontrolplaneInformers.Start(ctx.Done())
		kubeInformers.Start(ctx.Done())
		go check.Run(ctx, 1)
		go stopController.Run(ctx, 1)
		<-ctx.Done()
		return nil
	})
	config.DisableLeaderElection = true
	cmd := config.NewCommandWithContext(context.Background())
	cmd.Use = "check-endpoints"
	cmd.Short = "Checks that a tcp connection can be opened to one or more endpoints."
	return cmd
}
