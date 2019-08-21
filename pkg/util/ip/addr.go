package ip

import (
	"net"

	"github.com/pkg/errors"
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

// IterateIP returns n-th next IP4; n can be negative
func IterateIP4(ip net.IP, n int) net.IP {
	i := ip.To4()
	v := uint(i[0])<<24 + uint(i[1])<<16 + uint(i[2])<<8 + uint(i[3])

	if n >= 0 {
		v += uint(n)
	} else {
		v -= uint(-n)
	}

	return net.IPv4(byte((v>>24)&0xFF), byte((v>>16)&0xFF), byte((v>>8)&0xFF), byte(v&0xFF)).To4()
}

// ExpandNet returns subnet E of size twice the given subnet A (it just does `prefix -= 1`).
func ExpandNet(subnet net.IPNet) net.IPNet {
	ones, _ := subnet.Mask.Size()
	posByte := uint(ones / 8) // byte in which prefix ends
	posBit := uint(ones % 8)  // bit in which prefix ends

	// This will effectively do `prefix -= 1` by zeroing last set bit for expanded net
	expanded := net.IPNet{IP: make(net.IP, 4), Mask: make(net.IPMask, 4)}
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
		ranges = append(ranges, IPRange{FirstUsableIP(a), IterateIP4(b.IP, -1)})
	}

	if !lastIP(a).Equal(lastIP(b)) {
		ranges = append(ranges, IPRange{IterateIP4(lastIP(b), 1), LastUsableIP(a)})
	}

	return ranges
}
