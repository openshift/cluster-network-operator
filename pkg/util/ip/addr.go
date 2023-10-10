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
		if NetsOverlap(n, cidr) {
			return errors.Errorf("CIDRs %s and %s overlap",
				n.String(),
				cidr.String())
		}
	}
	p.cidrs = append(p.cidrs, cidr)
	return nil
}

// NetsOverlap return true if two nets overlap
func NetsOverlap(a, b net.IPNet) bool {
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
