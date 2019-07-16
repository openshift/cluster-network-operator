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

// This function splits given subnet into two even halves
func SplitNet(subnet net.IPNet) (net.IPNet, net.IPNet) {
	ones, _ := subnet.Mask.Size()
	posByte := uint(ones / 8)  // byte in which host part starts
	posBit := 7 - uint(ones%8) // bit of posByte in which the host part starts

	// This will effectively do `prefix += 1` on subnet by setting one more mask bit.
	// This gets us the "lower" part of the subnet.
	subnet.Mask[posByte] |= 1 << posBit

	// And for the "upper" part we just copy it and set that one added bit on the Address
	upper := net.IPNet{IP: make(net.IP, 4), Mask: make(net.IPMask, 4)}
	copy(upper.IP, subnet.IP)
	copy(upper.Mask, subnet.Mask)
	upper.IP[posByte] |= 1 << posBit

	return subnet, upper
}
