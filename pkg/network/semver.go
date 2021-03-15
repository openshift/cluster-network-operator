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
