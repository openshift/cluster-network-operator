//go:build !linux
// +build !linux

package network

func getDefaultMTU() (int, error) { return 1500, nil }
