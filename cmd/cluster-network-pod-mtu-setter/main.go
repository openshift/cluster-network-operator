package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"net"
	"os"
	"path"
	"time"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/fsnotify/fsnotify"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/network"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/vishvananda/netlink"
	"k8s.io/cri-api/pkg/apis/runtime/v1alpha2"
)

const (
	podIfName = "eth0"
	maxMTU    = 65000
	minMTU    = 576
)

func main() {
	var mtu *int
	var podNetwork *net.IPNet
	var runtimeEndpoint *string
	var cnoConfigPath *string
	var cnoConfigReadyPath *string
	var timeout time.Duration
	var dryRun *bool
	var checkDev *string
	var checkOffset *int
	var start time.Time
	var podNetworkStr *string
	var timeoutInt *int64
	
	cmd := cobra.Command{
		Use:   "cluster-network-pod-mtu-setter",
		Short: "change the mtu of running pods",
		PreRunE: func(cmd *cobra.Command, args []string) error {
			var err error
			start = time.Now()

			if *mtu < minMTU || *mtu > maxMTU {
				return fmt.Errorf("invalid mtu value %d, not between [%d,%d]", *mtu, minMTU, maxMTU)
			}

			if *podNetworkStr != "" {
				_, podNetwork, err = net.ParseCIDR(*podNetworkStr)
				if err != nil {
					return fmt.Errorf("invalid pod network: %v", err)
				}
			}

			timeout = time.Second * time.Duration(*timeoutInt)
			if timeout.Seconds() == 0 {
				timeout = time.Duration(math.MaxInt64)
			}

			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			err := checkMTUOffset(*mtu, *checkOffset, *checkDev)
			if err != nil {
				return err
			}
			if *cnoConfigPath != "" {
				return onConfigMTUSet(*cnoConfigPath, *cnoConfigReadyPath, *mtu, timeout, func() error {
					return setPodsMTU(*mtu, podNetwork, *runtimeEndpoint, start, *dryRun)
				})
			} else {
				return setPodsMTU(*mtu, podNetwork, *runtimeEndpoint, start, *dryRun)
			}
		},
	}

	runtimeEndpoint = cmd.PersistentFlags().String(
		"runtime-endpoint",
		fmt.Sprintf("first available from %v", defaultRuntimeEndpoints),
		"Endpoint of CRI container runtime service",
	)
	mtu = cmd.PersistentFlags().Int(
		"mtu",
		0,
		"`MTU` value to set",
	)
	podNetworkStr = cmd.PersistentFlags().String(
		"pod-network",
		"",
		"If provided, only perform the change for pods within this pod `NETWORK`",
	)
	dryRun = cmd.PersistentFlags().Bool(
		"dry-run",
		false,
		"Don't actually change the MTU",
	)
	cnoConfigPath = cmd.PersistentFlags().String(
		"cno-config-path",
		"",
		"If provided, wait until cno config at `PATH` is updated with the MTU value",
	)
	cnoConfigReadyPath = cmd.PersistentFlags().String(
		"cno-config-ready-path",
		"",
		"If provided, create file at `PATH` to signal that cno-config-path is being watched",
	)
	timeoutInt = cmd.PersistentFlags().Int64(
		"cno-config-timeout",
		0,
		"`SECONDS` to wait for cno config change",
	)
	checkOffset = cmd.PersistentFlags().Int(
		"mtu-check-offset",
		0,
		"Check that the provided MTU has minimum `OFFSET` with respect default gateway interface MTU",
	)
	checkDev = cmd.PersistentFlags().String(
		"mtu-check-dev",
		"",
		"Check MTU OFFSET for the provided `INTERFACE`",
	)

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func timeTrack(start time.Time, name string) {
	elapsed := time.Since(start)
	log.Printf("%s took %s", name, elapsed)
}

func setPodsMTU(mtu int, podNetwork *net.IPNet, runtimeEndpoint string, start time.Time, dryRun bool) error {
	defer timeTrack(time.Now(), "Setting pods MTU")
	intention := "Changing"
	if dryRun {
		intention = "Would change"
	}
	return forEveryPodStatus(runtimeEndpoint, func(pod *podStatus) error {
		if start.Before(time.Unix(0, pod.CreatedAt)) {
			log.Printf("Ignoring pod %s in namespace %s, created after start time of %s", pod.namespacedName(), pod.networkNamespace(), start.String())
			return nil
		}
		if pod.State != v1alpha2.PodSandboxState_SANDBOX_READY {
			log.Printf("Ignoring pod %s in namespace %s, invalid state %s", pod.namespacedName(), pod.networkNamespace(), v1alpha2.PodSandboxState_name[int32(pod.State)])
			return nil
		}
		if podNetwork != nil && !pod.isOnNetwork(podNetwork) {
			log.Printf("Ignoring pod %s in namespace %s, not in pod network %s", pod.namespacedName(), pod.networkNamespace(), podNetwork.String())
			return nil
		}
		log.Printf("%s MTU to %d for pod %s in namespace %v\n", intention, mtu, pod.namespacedName(), pod.networkNamespace())
		err := setVethMTU(pod.networkNamespace(), podIfName, mtu, dryRun)
		return errors.Wrapf(err, "failed to set MTU on pod with status %v", *pod)
	})
}

// checkMTUOffset checks there is a minimum offset between the
// provided interface (or the default interface if not specified) and
// the provided MTU.
func checkMTUOffset(mtu, offset int, iface string) error {
	var devMTU int
	var err error
	if iface == "" {
		iface = "default"
		devMTU, err = network.GetDefaultMTU()
	} else {
		devMTU, err = network.GetDevMTU(iface)
	}
	if err != nil {
		return errors.Wrapf(err, "can't get MTU value for provided device")
	}
	if devMTU < mtu+offset {
		return fmt.Errorf("MTU value %d is invalid as is above %s MTU %d and offset %d", mtu, iface, devMTU, offset)
	}

	return nil
}

// setVethMTU sets the MTU of the provided veth interface at
// the provided namespace path, as well as the peer MTU.
func setVethMTU(nsPath, name string, mtu int, dryRun bool) error {
	targetNs, err := ns.GetNS(nsPath)
	if err != nil {
		return errors.Wrapf(err, "could not get namespace %s", nsPath)
	}

	var peer int
	err = targetNs.Do(func(hostNs ns.NetNS) error {
		link, err := netlink.LinkByName(name)
		if err != nil {
			return errors.Wrapf(err, "could not get link with name %s", name)
		}

		veth, ok := link.(*netlink.Veth)
		if !ok {
			return fmt.Errorf("interface %s on namespace %s is not of type veth", name, nsPath)
		}

		if veth.MTU != mtu && !dryRun {
			err = netlink.LinkSetMTU(link, mtu)
			if err != nil {
				return errors.Wrapf(err, "could not set mtu %d on %s", mtu, name)
			}
		}

		peer, err = netlink.VethPeerIndex(veth)
		if err != nil {
			return errors.Wrapf(err, "could not get veth peer for %s", name)
		}

		return nil
	})
	if err != nil {
		return err
	}

	link, err := netlink.LinkByIndex(peer)
	if err != nil {
		return errors.Wrapf(err, "could not get link with index %d", peer)
	}

	if link.Attrs().MTU != mtu && !dryRun {
		err = netlink.LinkSetMTU(link, mtu)
		if err != nil {
			return errors.Wrapf(err, "could not set mtu %d on %s", mtu, link.Attrs().Name)
		}
	}

	return nil
}

// onConfigMTUSet waits until the provided MTU is set at config path
// to invoke the provided callback.
func onConfigMTUSet(configPath string, readyPath string, mtu int, timeout time.Duration, do func() error) error {
	configPath = path.Clean(configPath)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	done := make(chan bool)
	var watchErr error
	go func() {
		for {
			select {
			case <-watcher.Events:
				var actualMTU int
				actualMTU, watchErr = readMTU(configPath)
				if watchErr != nil {
					close(done)
					return
				}

				if actualMTU == mtu {
					log.Printf("Read actual MTU %d matches expected MTU %d, proceeding", actualMTU, mtu)
					watchErr = do()
					close(done)
					return
				}
				log.Printf("Read actual MTU %d does not match expected MTU %d, waiting", actualMTU, mtu)

			case err = <-watcher.Errors:
				watchErr = errors.Wrapf(err, "unexpected error waiting for config changes")
				close(done)
				return

			case <-time.After(timeout):
				watchErr = fmt.Errorf("timeout waiting for config changes")
				close(done)
				return
			}

			// watch again to work around:
			// https://github.com/fsnotify/fsnotify/issues/363
			_  = watcher.Remove(configPath)
			err = watcher.Add(configPath)
			if err != nil {
				watchErr = errors.Wrapf(err, "could not watch %s", configPath)
			}
		}
	}()

	err = watcher.Add(configPath)
	if err != nil {
		watchErr = errors.Wrapf(err, "could not watch %s", configPath)
	}

	if readyPath != "" {
		err = createReadyPath(readyPath)
		if err != nil {
			return errors.Wrapf(err, "could not create ready path %s", readyPath)
		}
	}
	log.Printf("Waiting for mtu %d to be set in config %s", mtu, configPath)

	// force an initial read
	watcher.Events <- fsnotify.Event{}

	<-done
	return watchErr
}

// readMTU reads the MTU of an OpenshiftSDN or OVNKubernetes
// configuration at the provided path
func readMTU(configPath string) (int, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	raw, err := ioutil.ReadAll(file)
	if err != nil {
		return 0, err
	}

	network := operv1.NetworkSpec{}
	err = json.Unmarshal(raw, &network)
	if err != nil {
		return 0, err
	}

	switch network.DefaultNetwork.Type {
	case operv1.NetworkTypeOpenShiftSDN:
		if network.DefaultNetwork.OpenShiftSDNConfig == nil || network.DefaultNetwork.OpenShiftSDNConfig.MTU == nil {
			return 0, fmt.Errorf("MTU value not available for OpenShiftSDN")
		}
		return int(*network.DefaultNetwork.OpenShiftSDNConfig.MTU), nil
	case operv1.NetworkTypeOVNKubernetes:
		if network.DefaultNetwork.OVNKubernetesConfig == nil || network.DefaultNetwork.OVNKubernetesConfig.MTU == nil {
			return 0, fmt.Errorf("MTU value not available for OVNKubernetes")
		}
		return int(*network.DefaultNetwork.OVNKubernetesConfig.MTU), nil
	default:
		return 0, fmt.Errorf("network type not supported: %s", string(network.DefaultNetwork.Type))
	}
}

func createReadyPath(readyPath string) error {
	_, err := os.Stat(readyPath)
	if err == nil {
		return fmt.Errorf("cannot signal readiness with path that already exists: %s", readyPath)
	}
	var file *os.File
	if os.IsNotExist(err) {
		file, err = os.Create(readyPath)
	}
	if err != nil {
		return fmt.Errorf("cannot signal readiness with path %s: %v", readyPath, err)
	}
	file.Close()
	return nil
}
