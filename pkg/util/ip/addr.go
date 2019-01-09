package ip

import (
	"net"

	"github.com/pkg/errors"
)

type IPPool struct {
	cidrs []net.IPNet
}

func (p *IPPool) Add(cidr net.IPNet) error {
	for _, n := range p.cidrs {
		if netsOverlap(n, cidr) {
			return errors.Errorf("CIDRs %s and %s overlap",
				n.String(),
				cidr.String())
		}
	}
	p.cidrs = append(p.cidrs, cidr)
	return nil
}

// netsOverlap return true if two nets overlap
func netsOverlap(a, b net.IPNet) bool {
	// ignore different families
	if len(a.IP) != len(b.IP) {
		return false
	}

	return a.Contains(b.IP) ||
		a.Contains(lastIP(b)) ||
		b.Contains(a.IP) ||
		b.Contains(lastIP(a))
}

// lastIP returns the last IP of a subnet
func lastIP(subnet net.IPNet) net.IP {
	var end net.IP
	for i := 0; i < len(subnet.IP); i++ {
		end = append(end, subnet.IP[i]|^subnet.Mask[i])
	}
	return end
}

// lastUsableIP returns second to last IP of a subnet
func LastUsableIP(subnet net.IPNet) net.IP {
	// FIXME(dulek): This should have proper IPv6 support, but for now whatever, Kuryr doesn't support it yet.
	// This gives us last IP of the subnet…
	ip := lastIP(subnet)
	// …and this will be second to last (last usable)
	ip[len(ip)-1] -= 1
	return ip
}

// FirstUsableIP returns second IP of a subnet
func FirstUsableIP(subnet net.IPNet) net.IP {
	// FIXME(dulek): This should have proper IPv6 support, but for now whatever, Kuryr doesn't support it yet.
	// This is first IP of the subnet…
	ip := subnet.IP
	// …and this will be second one (first usable)
	ip[len(ip)-1] += 1
	return ip
}
