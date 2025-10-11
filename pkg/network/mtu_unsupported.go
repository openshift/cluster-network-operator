//go:build !linux
// +build !linux

package network

const (
	MinMTUIPv4 uint32 = 576  // RFC 791
	MinMTUIPv6 uint32 = 1280 // RFC 8200
	MaxMTU     uint32 = 65536
)

func GetDefaultMTU() (int, error) { return 1500, nil }
