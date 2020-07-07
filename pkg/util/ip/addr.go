package ip

import (
	"net"

	"github.com/pkg/errors"
	utilnet "k8s.io/utils/net"
)

type IPPool struct {
	cidrs []net.IPNet
}

type IPRange struct {
	Start net.IP
	End   net.IP
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

// NetIncludes return true if net a includes net b
func NetIncludes(a, b net.IPNet) bool {
	// ignore different families
	if len(a.IP) != len(b.IP) {
		return false
	}

	return a.Contains(b.IP) && a.Contains(lastIP(b))
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
	ip := lastIP(subnet)
	if utilnet.IsIPv6(ip) {
		return ip
	}
	ip[len(ip)-1] -= 1
	return ip
}

// FirstUsableIP returns second IP of a subnet
func FirstUsableIP(subnet net.IPNet) net.IP {
	ip, _ := utilnet.GetIndexedIP(&subnet, 1)
	return ip
}

// ExpandNet returns subnet E of size twice the given subnet A (it just does `prefix -= 1`).
func ExpandNet(subnet net.IPNet) net.IPNet {
	ones, _ := subnet.Mask.Size()
	posByte := uint(ones / 8) // byte in which prefix ends
	posBit := uint(ones % 8)  // bit in which prefix ends

	// This will effectively do `prefix -= 1` by zeroing last set bit for expanded net
	var expanded net.IPNet
	if utilnet.IsIPv6(subnet.IP) {
		expanded = net.IPNet{IP: make(net.IP, 16), Mask: make(net.IPMask, 16)}
	} else {
		expanded = net.IPNet{IP: make(net.IP, 4), Mask: make(net.IPMask, 4)}
	}
	copy(expanded.IP, subnet.IP)
	copy(expanded.Mask, subnet.Mask)
	if posBit == 0 {
		expanded.Mask[posByte-1] &^= 1
	} else {
		expanded.Mask[posByte] &^= 1 << (8 - posBit)
	}
	expanded.IP = expanded.IP.Mask(expanded.Mask) // mask it in case there was 1 at the bit that is now in network part.

	return expanded
}

// NonOverlappingRanges return IP ranges of net a that are not overlapping with net b, assuming that net a includes
// whole net b. Only usable IP's of a are used.
func UsableNonOverlappingRanges(a, b net.IPNet) (ranges []IPRange) {
	if !a.IP.Equal(b.IP) {
		last_ip := utilnet.AddIPOffset(utilnet.BigForIP(b.IP), -1)
		//last_ip, err := utilnet.GetIndexedIP(&b, -1)
		ranges = append(ranges, IPRange{FirstUsableIP(a), last_ip})
	}

	if !lastIP(a).Equal(lastIP(b)) {
		first_ip := utilnet.AddIPOffset(utilnet.BigForIP(lastIP(b)), +1)
		//first_ip, _ := utilnet.GetIndexedIP(&net.IPNet{IP: lastIP(b), Mask: b.Mask}, 1)
		ranges = append(ranges, IPRange{first_ip, LastUsableIP(a)})
	}

	return ranges
}
