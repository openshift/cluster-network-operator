package network

import (
	"github.com/Masterminds/semver"
	"k8s.io/klog/v2"
)

type versionChange int

const (
	versionUpgrade   versionChange = -1
	versionSame      versionChange = 0
	versionDowngrade versionChange = 1
	versionUnknown   versionChange = 2
)

func (v versionChange) String() string {
	switch v {
	case versionUpgrade:
		return "upgrade"
	case versionSame:
		return "same"
	case versionDowngrade:
		return "downgrade"
	case versionUnknown:
		return "unknown"
	}
	klog.Warningf("unhandled versionChange value %v", v)
	return "UNHANDLED"
}

// compareVersions compares two semver versions
// if fromVersion is older than toVersion, returns versionOlder
// likewise, if fromVersion is newer, returns versionNewer
func compareVersions(fromVersion, toVersion string) versionChange {
	if fromVersion == toVersion {
		return versionSame
	}

	v1, err := semver.NewVersion(fromVersion)
	if err != nil {
		return versionUnknown
	}

	v2, err := semver.NewVersion(toVersion)
	if err != nil {
		return versionUnknown
	}

	return versionChange(v1.Compare(v2))
}

func isVersionGreaterThanOrEqualTo(version string, major int, minor int) bool {
	v, err := semver.NewVersion(version)
	if err != nil {
		klog.Errorf("failed to parse version %s: %v", version, err)
		return false
	}
	// 4.14 vs 5.13
	// 4.14 vs 4.13
	// 4.14 vs 3.15
	if v.Major() > int64(major) {
		return true
	} else if v.Major() == int64(major) {
		return v.Minor() >= int64(minor)
	} else {
		return false
	}
}

func isVersionLessThanOrEqualTo(version string, major int, minor int) bool {
	v, err := semver.NewVersion(version)
	if err != nil {
		klog.Errorf("failed to parse version %s: %v", version, err)
		return false
	}

	if v.Major() < int64(major) {
		return true
	} else if v.Major() == int64(major) {
		return v.Minor() <= int64(minor)
	} else {
		return false
	}
}
