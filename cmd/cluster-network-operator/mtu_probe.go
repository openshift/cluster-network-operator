package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/openshift/cluster-network-operator/pkg/network"
	"github.com/openshift/library-go/pkg/config/client"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// newMTUProberCommand returns a Command that determines the node's MTU
// and writes the result to a ConfigMap.
// This is used for cases where we don't trust the node on which the CNO runs,
// such as Hypershift.
func newMTUProberCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "probe-mtu",
		Short: "Probe for MTU, writing results in to a ConfigMap",
	}

	var kubeconfig string
	var namespace string
	var name string

	flags := cmd.Flags()
	flags.StringVar(&namespace, "namespace", "", "the namespace in which to write the config map")
	flags.StringVar(&name, "name", "", "the name of the ConfigMap to create")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if namespace == "" || name == "" {
			return fmt.Errorf("--namespace and --name are required")
		}
		if kubeconfig == "" {
			kubeconfig = os.Getenv("KUBECONFIG")
		}
		cfg, err := client.GetKubeConfigOrInClusterConfig(kubeconfig, nil)
		if err != nil {
			return err
		}
		clientSet, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return err
		}

		mtu, err := network.GetDefaultMTU()
		if err != nil {
			return err
		}
		fmt.Println("Detected node MTU:", mtu)

		cm := v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name,
			},
			Data: map[string]string{
				"mtu": strconv.Itoa(mtu),
			},
		}

		// Write the CM in the apiserver, retrying as needed.
		for tries := 0; tries < 10; tries++ {
			_, err = clientSet.CoreV1().ConfigMaps(namespace).Create(context.Background(), &cm, metav1.CreateOptions{})
			if err != nil && apierrors.IsAlreadyExists(err) {
				_, err = clientSet.CoreV1().ConfigMaps(namespace).Update(context.Background(), &cm, metav1.UpdateOptions{})
			}
			if err == nil {
				fmt.Println("Successfully set config map")
				break
			} else {
				fmt.Printf("Failed to write ConfigMap: %v", err)
				time.Sleep(10 * time.Second)
			}
		}

		return err
	}
	return cmd
}
