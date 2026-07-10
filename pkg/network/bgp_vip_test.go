package network

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/gomega"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildFRRConfigurationObjects(t *testing.T) {
	g := NewGomegaWithT(t)
	raw := `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513}],"apiVIPs":["192.168.111.5"],"ingressVIPs":["192.168.111.4"]}`
	var cfg bgpVIPConfigData
	g.Expect(json.Unmarshal([]byte(raw), &cfg)).To(Succeed())
	g.Expect(cfg.DefaultPeers).To(HaveLen(1))

	objs, err := buildFRRConfigurationObjects(cfg)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(HaveLen(1))
	g.Expect(objs[0].GetName()).To(Equal("bgp-vip"))
	// No node selector: the CR applies to all nodes.
	_, found, err := uns.NestedMap(objs[0].Object, "spec", "nodeSelector")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeFalse())
	routers, found, err := uns.NestedSlice(objs[0].Object, "spec", "bgp", "routers")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(routers).To(HaveLen(1))
	neighbors, found, err := uns.NestedSlice(routers[0].(map[string]interface{}), "neighbors")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(neighbors).To(HaveLen(1))

	// CRD prefixes would render as unconditional `network` statements,
	// bypassing the health gate.
	_, found, err = uns.NestedStringSlice(routers[0].(map[string]interface{}), "prefixes")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeFalse())
	neighbor := neighbors[0].(map[string]interface{})
	_, found, err = uns.NestedMap(neighbor, "toAdvertise")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeFalse())
	g.Expect(neighbor["address"]).To(Equal("192.168.111.1"))
	g.Expect(neighbor["asn"]).To(Equal(int64(64513)))

	rawConfig, found, err := uns.NestedString(objs[0].Object, "spec", "raw", "rawConfig")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(rawConfig).To(HavePrefix("router bgp"))
	g.Expect(rawConfig).NotTo(ContainSubstring("import-table"))
	g.Expect(rawConfig).To(ContainSubstring("router bgp 64512"))
	g.Expect(rawConfig).To(ContainSubstring("redistribute table-direct 198 route-map BGP-VIP-ROUTES-V4"))
	g.Expect(rawConfig).To(ContainSubstring("route-map BGP-VIP-ROUTES-V4 permit 10"))
	g.Expect(rawConfig).To(ContainSubstring("match ip address prefix-list BGP-VIP-PREFIXES-V4"))
	g.Expect(rawConfig).To(ContainSubstring("route-map BGP-VIP-ROUTES-V4 deny 20"))
	g.Expect(rawConfig).To(ContainSubstring("ip prefix-list BGP-VIP-PREFIXES-V4 seq 10 permit 192.168.111.5/32"))
	g.Expect(rawConfig).To(ContainSubstring("ip prefix-list BGP-VIP-PREFIXES-V4 seq 20 permit 192.168.111.4/32"))
	g.Expect(rawConfig).To(ContainSubstring("route-map 192.168.111.1-out permit 4000\n match ip address prefix-list BGP-VIP-PREFIXES-V4"))
	// No IPv6 VIPs: no v6 blocks at all.
	g.Expect(rawConfig).NotTo(ContainSubstring("-out permit 4001"))
	g.Expect(rawConfig).NotTo(ContainSubstring("address-family ipv6"))
	g.Expect(rawConfig).NotTo(ContainSubstring("BGP-VIP-ROUTES-V6"))
	g.Expect(rawConfig).NotTo(ContainSubstring("BGP-VIP-PREFIXES-V6"))
}

// TestBuildFRRConfigurationObjectsAllOptionalFields locks the
// installer-shaped config.json payload with every optional field set.
func TestBuildFRRConfigurationObjectsAllOptionalFields(t *testing.T) {
	g := NewGomegaWithT(t)
	raw := `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513,"password":"s3cret","bfdEnabled":"true","ebgpMultiHop":"true","holdTime":"90s","keepaliveTime":"30s"}],"communities":["64512:100"],"apiVIPs":["192.168.111.5"],"ingressVIPs":["192.168.111.4"],"hostOverrides":{"master-0":[{"peerAddress":"192.168.1.1","peerASN":64513}]}}`
	var cfg bgpVIPConfigData
	g.Expect(json.Unmarshal([]byte(raw), &cfg)).To(Succeed())
	g.Expect(cfg.DefaultPeers).To(HaveLen(1))
	g.Expect(cfg.HostOverrides).To(HaveLen(1))
	g.Expect(cfg.HostOverrides["master-0"]).To(HaveLen(1))

	objs, err := buildFRRConfigurationObjects(cfg)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(HaveLen(1))

	routers, found, err := uns.NestedSlice(objs[0].Object, "spec", "bgp", "routers")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(routers).To(HaveLen(1))
	neighbors, found, err := uns.NestedSlice(routers[0].(map[string]interface{}), "neighbors")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(neighbors).To(HaveLen(1))

	neighbor := neighbors[0].(map[string]interface{})
	// The installer serializes ebgpMultiHop as a string; the CR wants a bool.
	g.Expect(neighbor["ebgpMultiHop"]).To(Equal(true))
	g.Expect(neighbor["holdTime"]).To(Equal("90s"))
	g.Expect(neighbor["keepaliveTime"]).To(Equal("30s"))
	g.Expect(neighbor["password"]).To(Equal("s3cret"))
	g.Expect(neighbor["bfdProfile"]).To(Equal("vip-bfd"))
	_, found, err = uns.NestedMap(neighbor, "toAdvertise")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeFalse())
	_, found, err = uns.NestedStringSlice(routers[0].(map[string]interface{}), "prefixes")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeFalse())

	rawConfig, found, err := uns.NestedString(objs[0].Object, "spec", "raw", "rawConfig")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(rawConfig).To(HavePrefix("router bgp"))
	g.Expect(rawConfig).NotTo(ContainSubstring("import-table"))
	g.Expect(rawConfig).To(ContainSubstring("redistribute table-direct 198 route-map BGP-VIP-ROUTES-V4"))
	g.Expect(rawConfig).To(ContainSubstring("route-map BGP-VIP-ROUTES-V4 permit 10"))
	g.Expect(rawConfig).To(ContainSubstring(" set community 64512:100 additive"))
	g.Expect(rawConfig).To(ContainSubstring("route-map BGP-VIP-ROUTES-V4 deny 20"))
	g.Expect(rawConfig).To(ContainSubstring("ip prefix-list BGP-VIP-PREFIXES-V4 seq 10 permit 192.168.111.5/32"))
	g.Expect(rawConfig).To(ContainSubstring("ip prefix-list BGP-VIP-PREFIXES-V4 seq 20 permit 192.168.111.4/32"))
	g.Expect(rawConfig).To(ContainSubstring("route-map 192.168.111.1-out permit 4000\n match ip address prefix-list BGP-VIP-PREFIXES-V4"))

	bfdProfiles, found, err := uns.NestedSlice(objs[0].Object, "spec", "bgp", "bfdProfiles")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(bfdProfiles).To(HaveLen(1))
}

// TestBuildFRRConfigurationObjectsDualStack locks the per-family rendering.
func TestBuildFRRConfigurationObjectsDualStack(t *testing.T) {
	g := NewGomegaWithT(t)
	raw := `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513}],"apiVIPs":["192.168.111.5","fd2e:6f44:5dd8::5"],"ingressVIPs":["192.168.111.4","fd2e:6f44:5dd8::4"]}`
	var cfg bgpVIPConfigData
	g.Expect(json.Unmarshal([]byte(raw), &cfg)).To(Succeed())

	objs, err := buildFRRConfigurationObjects(cfg)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(HaveLen(1))

	rawConfig, found, err := uns.NestedString(objs[0].Object, "spec", "raw", "rawConfig")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(rawConfig).To(HavePrefix("router bgp"))
	g.Expect(rawConfig).NotTo(ContainSubstring("import-table"))
	g.Expect(rawConfig).To(ContainSubstring("redistribute table-direct 198 route-map BGP-VIP-ROUTES-V4"))
	g.Expect(rawConfig).To(ContainSubstring("redistribute table-direct 198 route-map BGP-VIP-ROUTES-V6"))
	g.Expect(rawConfig).To(ContainSubstring("route-map BGP-VIP-ROUTES-V6 permit 10"))
	g.Expect(rawConfig).To(ContainSubstring("match ipv6 address prefix-list BGP-VIP-PREFIXES-V6"))
	g.Expect(rawConfig).To(ContainSubstring("route-map BGP-VIP-ROUTES-V6 deny 20"))
	g.Expect(rawConfig).To(ContainSubstring("ip prefix-list BGP-VIP-PREFIXES-V4 seq 10 permit 192.168.111.5/32"))
	g.Expect(rawConfig).To(ContainSubstring("ip prefix-list BGP-VIP-PREFIXES-V4 seq 20 permit 192.168.111.4/32"))
	g.Expect(rawConfig).To(ContainSubstring("ipv6 prefix-list BGP-VIP-PREFIXES-V6 seq 10 permit fd2e:6f44:5dd8::5/128"))
	g.Expect(rawConfig).To(ContainSubstring("ipv6 prefix-list BGP-VIP-PREFIXES-V6 seq 20 permit fd2e:6f44:5dd8::4/128"))
	g.Expect(rawConfig).To(ContainSubstring("route-map 192.168.111.1-out permit 4000\n match ip address prefix-list BGP-VIP-PREFIXES-V4"))
	g.Expect(rawConfig).To(ContainSubstring("route-map 192.168.111.1-out permit 4001\n match ipv6 address prefix-list BGP-VIP-PREFIXES-V6"))
}

func TestValidateBGPVIPConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	valid := bgpVIPConfigData{
		LocalASN:     64512,
		DefaultPeers: []bgpVIPPeer{{PeerAddress: "192.168.111.1", PeerASN: 64513}},
		APIVIPs:      []string{"192.168.111.5"},
		IngressVIPs:  []string{"fd2e:6f44:5dd8::4"},
		HostOverrides: map[string][]bgpVIPPeer{
			"master-0": {{PeerAddress: "192.168.111.2", PeerASN: 64513}},
		},
	}
	g.Expect(validateBGPVIPConfig(valid)).To(Succeed())

	badVIP := valid
	badVIP.APIVIPs = []string{"192.168.111.5\n router bgp 1"}
	g.Expect(validateBGPVIPConfig(badVIP)).NotTo(Succeed())

	badPeer := valid
	badPeer.DefaultPeers = []bgpVIPPeer{{PeerAddress: "not-an-ip", PeerASN: 64513}}
	g.Expect(validateBGPVIPConfig(badPeer)).NotTo(Succeed())

	badOverride := valid
	badOverride.HostOverrides = map[string][]bgpVIPPeer{"master-0": {{PeerAddress: "", PeerASN: 1}}}
	g.Expect(validateBGPVIPConfig(badOverride)).NotTo(Succeed())

	badDuration := valid
	badDuration.DefaultPeers = []bgpVIPPeer{{PeerAddress: "192.168.111.1", PeerASN: 64513, HoldTime: "ninety"}}
	g.Expect(validateBGPVIPConfig(badDuration)).NotTo(Succeed())

	badBool := valid
	badBool.DefaultPeers = []bgpVIPPeer{{PeerAddress: "192.168.111.1", PeerASN: 64513, BFDEnabled: "True"}}
	g.Expect(validateBGPVIPConfig(badBool)).NotTo(Succeed())

	badLocalASN := valid
	badLocalASN.LocalASN = 0
	g.Expect(validateBGPVIPConfig(badLocalASN)).NotTo(Succeed())

	badPeerASN := valid
	badPeerASN.DefaultPeers = []bgpVIPPeer{{PeerAddress: "192.168.111.1", PeerASN: 4294967296}}
	g.Expect(validateBGPVIPConfig(badPeerASN)).NotTo(Succeed())

	goodCommunity := valid
	goodCommunity.Communities = []string{"64512:100", "64512:100:200"}
	g.Expect(validateBGPVIPConfig(goodCommunity)).To(Succeed())

	badCommunity := valid
	badCommunity.Communities = []string{"no-export\n router bgp 1"}
	g.Expect(validateBGPVIPConfig(badCommunity)).NotTo(Succeed())
}
