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

// This function returns subnet E of size twice the given subnet A (it just does `prefix -= 1`) and
// the subnet that includes all the IP's A was expanded with to form E.
func ExpandNet(subnet net.IPNet) (net.IPNet, net.IPNet) {
	// First determine if `subnet` will be upper or lower half double the `subnet`
	ones, _ := subnet.Mask.Size()
	posByte := uint(ones / 8) // byte in which prefix ends
	posBit := uint(ones % 8)  // bit in which prefix ends

	rest := net.IPNet{IP: make(net.IP, 4), Mask: make(net.IPMask, 4)}
	copy(rest.IP, subnet.IP)
	copy(rest.Mask, subnet.Mask)
	// To get the "other" part of expanded net we need to just toggle the last
	// bit of the network part.
	if posBit == 0 { // we need to look at previous byte here
		rest.IP[posByte-1] ^= 1
	} else {
		rest.IP[posByte] ^= 1 << (8 - posBit)
	}

	// This will effectively do `prefix -= 1` on subnet by zeroing last set bit.
	if posBit == 0 {
		subnet.Mask[posByte-1] &^= 1
	} else {
		subnet.Mask[posByte] &^= 1 << (8 - posBit)
	}
	subnet.IP = subnet.IP.Mask(subnet.Mask) // mask it in case there was 1 at the bit that is now in network part.

	return subnet, rest
}
