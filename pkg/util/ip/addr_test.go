package ip

import (
	"net"
	"testing"

	. "github.com/onsi/gomega"
)

func TestAddrPool(t *testing.T) {
	g := NewGomegaWithT(t)
	testcases := []struct {
		cidr string
		ok   bool
	}{
		{
			"10.0.0.0/24",
			true,
		},
		{
			"10.0.0.0/24",
			false,
		},
		{
			"10.0.2.0/24",
			true,
		},
		{
			"fe80:1:2:3::/64",
			true,
		},
		{
			"fe80:1:2:3:4::/80",
			false,
		},
	}

	pool := IPPool{}
	for idx, tc := range testcases {
		_, cidr, err := net.ParseCIDR(tc.cidr)
		g.Expect(err).NotTo(HaveOccurred())
		err = pool.Add(*cidr)
		if tc.ok {
			g.Expect(err).NotTo(HaveOccurred(), "tc %d", idx)
		} else {
			g.Expect(err).To(HaveOccurred(), "tc %d", idx)
		}
	}
}

func TestNetsOverlap(t *testing.T) {
	g := NewGomegaWithT(t)
	testcases := []struct {
		cidr1    string
		cidr2    string
		expected bool
	}{
		{
			"10.0.0.0/24",
			"10.0.1.0/24",
			false,
		},
		//
		{
			"10.0.0.0/22",
			"10.0.0.0/24",
			true,
		},
		{
			"10.0.0.0/24",
			"10.0.0.0/22",
			true,
		},
		{
			"10.0.0.0/22",
			"10.0.3.0/24",
			true,
		},
		{
			"fe80:1:2:3::/64",
			"fe80:1:2:3:4::/80",
			true,
		},
	}

	for _, tc := range testcases {
		_, c1, err := net.ParseCIDR(tc.cidr1)
		g.Expect(err).NotTo(HaveOccurred())
		_, c2, err := net.ParseCIDR(tc.cidr2)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(NetsOverlap(*c1, *c2)).To(Equal(tc.expected))

	}
}

func TestNetIncludes(t *testing.T) {
	g := NewGomegaWithT(t)
	testcases := []struct {
		cidr1    string
		cidr2    string
		expected bool
	}{
		{
			"10.0.0.0/24",
			"10.0.1.0/24",
			false,
		},
		//
		{
			"10.0.0.0/22",
			"10.0.0.0/24",
			true,
		},
		{
			"10.0.0.0/24",
			"10.0.0.0/22",
			false,
		},
		{
			"10.0.0.0/22",
			"10.0.3.0/24",
			true,
		},
		{
			"172.30.0.0/15",
			"172.31.0.0/16",
			true,
		},
		{
			"172.30.0.0/15",
			"172.30.0.0/16",
			true,
		},
		{
			"172.30.0.0/16",
			"172.30.0.0/16",
			true,
		},
	}

	for _, tc := range testcases {
		_, c1, err := net.ParseCIDR(tc.cidr1)
		g.Expect(err).NotTo(HaveOccurred())
		_, c2, err := net.ParseCIDR(tc.cidr2)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(NetIncludes(*c1, *c2)).To(Equal(tc.expected))

	}
}

func TestLastIP(t *testing.T) {
	g := NewGomegaWithT(t)

	testcases := []struct {
		cidr     string
		expected string
	}{
		{
			"10.0.0.0/24",
			"10.0.0.255",
		},
		{
			"10.0.0.128/30",
			"10.0.0.131",
		},
		{
			"fe80:1:2:3::/64",
			"fe80:1:2:3:ffff:ffff:ffff:ffff",
		},
	}

	for _, tc := range testcases {
		_, cidr, err := net.ParseCIDR(tc.cidr)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(lastIP(*cidr).String()).To(Equal(tc.expected))
	}
}

func TestLastUsableIP(t *testing.T) {
	g := NewGomegaWithT(t)

	testcases := []struct {
		cidr     string
		expected string
	}{
		{
			"10.0.0.0/24",
			"10.0.0.254",
		},
		{
			"10.0.0.128/30",
			"10.0.0.130",
		},
	}

	for _, tc := range testcases {
		_, cidr, err := net.ParseCIDR(tc.cidr)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(LastUsableIP(*cidr).String()).To(Equal(tc.expected))
	}
}

func TestFirstUsableIP(t *testing.T) {
	g := NewGomegaWithT(t)

	testcases := []struct {
		cidr     string
		expected string
	}{
		{
			"10.0.0.0/24",
			"10.0.0.1",
		},
		{
			"10.0.0.128/30",
			"10.0.0.129",
		},
	}

	for _, tc := range testcases {
		_, cidr, err := net.ParseCIDR(tc.cidr)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(FirstUsableIP(*cidr).String()).To(Equal(tc.expected))
	}
}

func TestExpandNet(t *testing.T) {
	g := NewGomegaWithT(t)

	testcases := []struct {
		cidr           string
		expectedDouble string
	}{
		{
			"10.0.0.0/24",
			"10.0.0.0/23",
		},
		{
			"10.0.0.128/28",
			"10.0.0.128/27",
		},
		{
			"172.31.0.0/16",
			"172.30.0.0/15",
		},
		{
			"172.30.0.0/16",
			"172.30.0.0/15",
		},
		{
			"10.0.1.0/24",
			"10.0.0.0/23",
		},
	}

	for _, tc := range testcases {
		_, cidr, err := net.ParseCIDR(tc.cidr)
		g.Expect(err).NotTo(HaveOccurred())
		e := ExpandNet(*cidr)
		g.Expect(e.String()).To(Equal(tc.expectedDouble))
	}
}

func TestIterateIP4(t *testing.T) {
	g := NewGomegaWithT(t)

	testcases := []struct {
		ip       string
		n        int
		expected string
	}{
		{"10.0.0.0", 1, "10.0.0.1"},
		{"10.0.0.129", 1, "10.0.0.130"},
		{"10.0.0.1", -1, "10.0.0.0"},
		{"172.30.255.254", -1, "172.30.255.253"},
		{"10.0.0.0", -1, "9.255.255.255"},
	}

	for _, tc := range testcases {
		ip := net.ParseIP(tc.ip)
		g.Expect(IterateIP4(ip, tc.n).String()).To(Equal(tc.expected))
	}
}

func TestUsableNonOverlappingRanges(t *testing.T) {
	g := NewGomegaWithT(t)

	type strRange struct {
		start string
		end   string
	}

	testcases := []struct {
		a        string
		b        string
		expected []strRange
	}{
		{"172.30.0.0/15", "172.30.0.0/16", []strRange{{"172.31.0.0", "172.31.255.254"}}},
		{"172.30.0.0/15", "172.31.0.0/16", []strRange{{"172.30.0.1", "172.30.255.255"}}},
		{"172.30.0.0/14", "172.30.0.0/16", []strRange{{"172.28.0.1", "172.29.255.255"}, {"172.31.0.0", "172.31.255.254"}}},
	}

	for _, tc := range testcases {
		_, a, err := net.ParseCIDR(tc.a)
		g.Expect(err).NotTo(HaveOccurred())
		_, b, err := net.ParseCIDR(tc.b)
		g.Expect(err).NotTo(HaveOccurred())

		ranges := UsableNonOverlappingRanges(*a, *b)

		for i, r := range ranges {
			g.Expect(r.Start.String()).To(Equal(tc.expected[i].start))
			g.Expect(r.End.String()).To(Equal(tc.expected[i].end))
		}
	}
}
