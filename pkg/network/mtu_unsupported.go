// +build !linux

package network

func GetDefaultMTU() (int, error) { return 1500, nil }
