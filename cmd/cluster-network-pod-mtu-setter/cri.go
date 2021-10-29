package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"time"

	spec "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	cri "k8s.io/cri-api/pkg/apis/runtime/v1alpha2"
)

const (
	defaultTimeout = 2 * time.Second
)

var defaultRuntimeEndpoints = []string{
	"unix:///run/crio/crio.sock",
	"unix:///run/containerd/containerd.sock",
	"unix:///var/run/dockershim.sock",
}

func getAddressAndDialer(endpoint string) (string, func(ctx context.Context, addr string) (net.Conn, error), error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", nil, err
	}
	if u.Scheme != "unix" && u.Scheme != "" {
		return "", nil, fmt.Errorf("only support unix socket endpoint")
	}

	dial := func(ctx context.Context, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", addr)
	}
	return u.Path, dial, nil
}

func getConnection(endPoints []string) (*grpc.ClientConn, error) {
	if len(endPoints) == 0 {
		return nil, fmt.Errorf("endpoint is not set")
	}
	endPointsLen := len(endPoints)
	var conn *grpc.ClientConn
	for indx, endPoint := range endPoints {
		log.Printf("connect using endpoint '%s' with '%s' timeout", endPoint, defaultTimeout)
		addr, dialer, err := getAddressAndDialer(endPoint)
		if err != nil {
			if indx == endPointsLen-1 {
				return nil, err
			}
			log.Printf("%v", err)
			continue
		}

		context, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		defer cancel()

		conn, err = grpc.DialContext(context, addr, grpc.WithInsecure(), grpc.WithBlock(), grpc.WithContextDialer(dialer))
		if err != nil {
			errMsg := errors.Wrapf(err, "connect endpoint '%s', make sure you are running as root and the endpoint has been started", endPoint)
			if indx == endPointsLen-1 {
				return nil, errMsg
			}
			log.Printf("%v", errMsg)
		} else {
			log.Printf("connected successfully using endpoint: %s", endPoint)
			break
		}
	}
	return conn, nil
}

func getRuntimeClientConnection(runtimeEndpoint string) (*grpc.ClientConn, error) {
	if runtimeEndpoint == "" {
		return getConnection(defaultRuntimeEndpoints)
	}
	return getConnection([]string{runtimeEndpoint})
}

func getRuntimeClient(runtimeEndpoint string) (cri.RuntimeServiceClient, *grpc.ClientConn, error) {
	// Set up a connection to the server.
	conn, err := getRuntimeClientConnection(runtimeEndpoint)
	if err != nil {
		return nil, nil, errors.Wrap(err, "connect")
	}
	runtimeClient := cri.NewRuntimeServiceClient(conn)
	return runtimeClient, conn, nil
}

func closeConnection(conn *grpc.ClientConn) error {
	if conn == nil {
		return nil
	}
	return conn.Close()
}

// ListPodSandboxes sends a ListPodSandboxRequest to the server, and parses
// the returned ListPodSandboxResponse.
func listPods(client cri.RuntimeServiceClient) ([]*cri.PodSandbox, error) {
	request := &cri.ListPodSandboxRequest{}
	r, err := client.ListPodSandbox(context.Background(), request)
	if err != nil {
		return nil, err
	}

	return r.Items, nil
}

type podStatus struct {
	*cri.PodSandboxStatus
	RuntimeSpec spec.Spec `json:"runtimeSpec,omitempty"`
}

func (ps *podStatus) namespacedName() string {
	return fmt.Sprintf("%s/%s", ps.Metadata.Namespace, ps.Metadata.Name)
}

func (ps *podStatus) isOnNetwork(network *net.IPNet) bool {
	var ip net.IP
	if ps.Network != nil {
		ip = net.ParseIP(ps.Network.Ip)
	}
	return ip != nil && network.Contains(ip)
}

func (ps *podStatus) networkNamespace() string {
	if ps.RuntimeSpec.Linux != nil {
		for _, ns := range ps.RuntimeSpec.Linux.Namespaces {
			if ns.Type == spec.NetworkNamespace {
				return ns.Path
			}
		}
	}
	return ""
}

// PodSandboxStatus sends a PodSandboxStatusRequest to the server, and parses
// the returned PodSandboxStatusResponse.
func getPodStatus(client cri.RuntimeServiceClient, id string) (*podStatus, error) {
	request := &cri.PodSandboxStatusRequest{
		PodSandboxId: id,
		Verbose:      true,
	}
	r, err := client.PodSandboxStatus(context.Background(), request)
	if err != nil {
		return nil, err
	}

	status := podStatus{}
	err = json.Unmarshal([]byte(r.Info["info"]), &status)
	if err != nil {
		return nil, err
	}
	status.PodSandboxStatus = r.Status

	return &status, nil
}

func forEveryPodStatusWithClient(client cri.RuntimeServiceClient, do func(pod *podStatus) error) error {
	pods, err := listPods(client)
	if err != nil {
		return err
	}

	for _, pod := range pods {
		status, err := getPodStatus(client, pod.Id)
		if err != nil {
			return err
		}

		err = do(status)
		if err != nil {
			return err
		}
	}

	return nil
}

func forEveryPodStatus(runtimeEndpoint string, do func(pod *podStatus) error) error {
	client, conn, err := getRuntimeClient(runtimeEndpoint)
	if err != nil {
		return err
	}
	defer func() {
		_ = closeConnection(conn)
	}()
		
	return forEveryPodStatusWithClient(client, do)
}
