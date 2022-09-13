package operconfig

import (
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type fakeCNOClient struct {
	cnoclient.Client
	clusterClient cnoclient.ClusterClient
}

func (f *fakeCNOClient) Default() cnoclient.ClusterClient {
	return f.clusterClient
}

type fakeClusterClient struct {
	cnoclient.ClusterClient
	crclient crclient.Client
}

func (f *fakeClusterClient) CRClient() crclient.Client {
	return f.crclient
}
