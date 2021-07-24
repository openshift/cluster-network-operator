package proxyconfig

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var cfgMapKey = "install-config"
var cfgMapData = `
    controlPlane:
      replicas: 3
    networking:
      machineCIDR: 10.0.0.0/16
`

func proxyConfig() *configv1.Proxy {
	return &configv1.Proxy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-proxy",
		},
		Spec: configv1.ProxySpec{
			HTTPProxy:  "http://user:pswd@test.proxy.com:1234",
			HTTPSProxy: "https://user:pswd@test.secure-proxy.com:5678",
		},
	}
}

func proxyConfigWithNoProxy(noProxy string) *configv1.Proxy {
	proxy := proxyConfig()
	proxy.Spec.NoProxy = noProxy
	return proxy
}

func infraConfig(platform configv1.PlatformType, domain, region string) *configv1.Infrastructure {
	platformStatus := &configv1.PlatformStatus{}
	switch platform {
	case configv1.AWSPlatformType:
		platformStatus = &configv1.PlatformStatus{
			Type: configv1.AWSPlatformType,
			AWS: &configv1.AWSPlatformStatus{
				Region: region,
			},
		}
	case configv1.GCPPlatformType:
		platformStatus = &configv1.PlatformStatus{
			Type: configv1.GCPPlatformType,
			GCP: &configv1.GCPPlatformStatus{
				Region: region,
			},
		}
	}
	return &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-infra",
		},
		Status: configv1.InfrastructureStatus{
			APIServerURL:         "https://api." + domain + ":6443",
			APIServerInternalURL: "https://api-int." + domain + ":6443",
			PlatformStatus:       platformStatus,
			EtcdDiscoveryDomain:  domain,
		},
	}
}

func netConfig(cluster string, svcNet []string) *configv1.Network {
	clusterNet := configv1.ClusterNetworkEntry{CIDR: cluster}
	return &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-net",
		},
		Status: configv1.NetworkStatus{
			ClusterNetwork: []configv1.ClusterNetworkEntry{clusterNet},
			ServiceNetwork: svcNet,
		},
	}
}

func cfgMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cfgmap",
			Namespace: "test-ns",
		},
	}
}

func cfgMapWithInstallConfig(key, data string) *corev1.ConfigMap {
	cfgMap := cfgMap()
	cfgMap.Data = map[string]string{key: data}
	return cfgMap
}

func TestMergeUserSystemNoProxy(t *testing.T) {
	type args struct {
		proxy   *configv1.Proxy
		infra   *configv1.Infrastructure
		network *configv1.Network
		cluster *corev1.ConfigMap
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{name: "valid proxy config with aws provider",
			args: args{
				proxy:   proxyConfig(),
				infra:   infraConfig(configv1.AWSPlatformType, "test.cluster.com", "us-west-2"),
				network: netConfig("10.128.0.0/14", []string{"172.30.0.0/16"}),
				cluster: cfgMapWithInstallConfig(cfgMapKey, cfgMapData),
			},
			want: ".cluster.local,.svc,.us-west-2.compute.internal,10.0.0.0/16,10.128.0.0/14,127.0.0.1," +
				"169.254.169.254,172.30.0.0/16,api-int.test.cluster.com,localhost",
			wantErr: false,
		},
		{name: "valid proxy config with gcp provider",
			args: args{
				proxy:   proxyConfig(),
				infra:   infraConfig(configv1.GCPPlatformType, "test.cluster.com", "us-west2"),
				network: netConfig("10.128.0.0/14", []string{"172.30.0.0/16"}),
				cluster: cfgMapWithInstallConfig(cfgMapKey, cfgMapData),
			},
			want: ".cluster.local,.svc,10.0.0.0/16,10.128.0.0/14,127.0.0.1,169.254.169.254,172.30.0.0/16," +
				"api-int.test.cluster.com,localhost,metadata,metadata.google.internal,metadata.google.internal.",
			wantErr: false,
		},
		{name: "valid proxy config using us-east-1 aws region",
			args: args{
				proxy:   proxyConfig(),
				infra:   infraConfig(configv1.AWSPlatformType, "test.cluster.com", "us-east-1"),
				network: netConfig("10.128.0.0/14", []string{"172.30.0.0/16"}),
				cluster: cfgMapWithInstallConfig(cfgMapKey, cfgMapData),
			},
			want: ".cluster.local,.ec2.internal,.svc,10.0.0.0/16,10.128.0.0/14,127.0.0.1," +
				"169.254.169.254,172.30.0.0/16,api-int.test.cluster.com,localhost",
			wantErr: false,
		},
		{name: "valid proxy config with single user noProxy",
			args: args{
				proxy:   proxyConfigWithNoProxy("172.30.0.1"),
				infra:   infraConfig(configv1.AWSPlatformType, "test.cluster.com", "us-west-2"),
				network: netConfig("10.128.0.0/14", []string{"172.30.0.0/16"}),
				cluster: cfgMapWithInstallConfig(cfgMapKey, cfgMapData),
			},
			want: ".cluster.local,.svc,.us-west-2.compute.internal,10.0.0.0/16,10.128.0.0/14,127.0.0.1," +
				"169.254.169.254,172.30.0.0/16,172.30.0.1,api-int.test.cluster.com,localhost",
			wantErr: false,
		},
		{name: "valid proxy config with single user noProxy dual stack",
			args: args{
				proxy:   proxyConfigWithNoProxy("172.30.0.1"),
				infra:   infraConfig(configv1.AWSPlatformType, "test.cluster.com", "us-west-2"),
				network: netConfig("10.128.0.0/14", []string{"172.30.0.0/16", "2001:db8::/32"}),
				cluster: cfgMapWithInstallConfig(cfgMapKey, cfgMapData),
			},
			want: ".cluster.local,.svc,.us-west-2.compute.internal,10.0.0.0/16,10.128.0.0/14,127.0.0.1," +
				"169.254.169.254,172.30.0.0/16,172.30.0.1,2001:db8::/32,api-int.test.cluster.com,localhost",
			wantErr: false,
		},
		{name: "valid proxy config with multiple user noProxy",
			args: args{
				proxy:   proxyConfigWithNoProxy("172.30.0.1,.foo.test.com,199.161.0.0/16"),
				infra:   infraConfig(configv1.AWSPlatformType, "test.cluster.com", "us-west-2"),
				network: netConfig("10.128.0.0/14", []string{"172.30.0.0/16"}),
				cluster: cfgMapWithInstallConfig(cfgMapKey, cfgMapData),
			},
			want: ".cluster.local,.foo.test.com,.svc,.us-west-2.compute.internal,10.0.0.0/16,10.128.0.0/14,127.0.0.1," +
				"169.254.169.254,172.30.0.0/16,172.30.0.1,199.161.0.0/16,api-int.test.cluster.com,localhost",
			wantErr: false,
		},
		{name: "valid proxy config with multiple user noProxy dual stack",
			args: args{
				proxy:   proxyConfigWithNoProxy("172.30.0.1,.foo.test.com,199.161.0.0/16"),
				infra:   infraConfig(configv1.AWSPlatformType, "test.cluster.com", "us-west-2"),
				network: netConfig("10.128.0.0/14", []string{"172.30.0.0/16", "2001:db8::/32"}),
				cluster: cfgMapWithInstallConfig(cfgMapKey, cfgMapData),
			},
			want: ".cluster.local,.foo.test.com,.svc,.us-west-2.compute.internal,10.0.0.0/16,10.128.0.0/14,127.0.0.1," +
				"169.254.169.254,172.30.0.0/16,172.30.0.1,199.161.0.0/16,2001:db8::/32,api-int.test.cluster.com,localhost",
			wantErr: false,
		},
		{name: "invalid api server url",
			args: args{
				proxy:   proxyConfigWithNoProxy("172.30.0.1."),
				infra:   infraConfig(configv1.AWSPlatformType, "^&", "us-west-2"),
				network: netConfig("10.128.0.0/14", []string{"172.30.0.0/16"}),
				cluster: cfgMapWithInstallConfig(cfgMapKey, cfgMapData),
			},
			wantErr: true,
		},
		{name: "invalid missing service network",
			args: args{
				proxy:   proxyConfigWithNoProxy("172.30.0.1."),
				infra:   infraConfig(configv1.AWSPlatformType, "^&", "us-west-2"),
				network: netConfig("10.128.0.0/14", nil),
				cluster: cfgMapWithInstallConfig(cfgMapKey, cfgMapData),
			},
			wantErr: true,
		},
		{name: "invalid missing cluster network",
			args: args{
				proxy:   proxyConfigWithNoProxy("172.30.0.1."),
				infra:   infraConfig(configv1.AWSPlatformType, "^&", "us-west-2"),
				network: netConfig("", []string{"172.30.0.0/16"}),
				cluster: cfgMapWithInstallConfig(cfgMapKey, cfgMapData),
			},
			wantErr: true,
		},
		{name: "invalid empty configmap",
			args: args{
				proxy:   proxyConfigWithNoProxy("172.30.0.1."),
				infra:   infraConfig(configv1.AWSPlatformType, "^&", "us-west-2"),
				network: netConfig("", []string{"172.30.0.0/16"}),
				cluster: cfgMap(),
			},
			wantErr: true,
		},
		{name: "invalid configmap key",
			args: args{
				proxy:   proxyConfigWithNoProxy("172.30.0.1."),
				infra:   infraConfig(configv1.AWSPlatformType, "^&", "us-west-2"),
				network: netConfig("", []string{"172.30.0.0/16"}),
				cluster: cfgMapWithInstallConfig("bad-key", cfgMapData),
			},
			wantErr: true,
		},
		{name: "invalid configmap data",
			args: args{
				proxy:   proxyConfigWithNoProxy("172.30.0.1."),
				infra:   infraConfig(configv1.AWSPlatformType, "^&", "us-west-2"),
				network: netConfig("", []string{"172.30.0.0/16"}),
				cluster: cfgMapWithInstallConfig("bad-key", "bad data"),
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MergeUserSystemNoProxy(tt.args.proxy, tt.args.infra, tt.args.network, tt.args.cluster)
			if (err != nil) != tt.wantErr {
				t.Errorf("MergeUserSystemNoProxy() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("MergeUserSystemNoProxy() got = %v, want %v", got, tt.want)
			}
		})
	}
}
