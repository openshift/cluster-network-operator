package version

import (
	"github.com/Masterminds/semver"
	"k8s.io/klog/v2"
)

type versionChange int

const (
	VersionUpgrade   versionChange = -1
	VersionSame      versionChange = 0
	VersionDowngrade versionChange = 1
	VersionUnknown   versionChange = 2
)

func (v versionChange) String() string {
	switch v {
	case VersionUpgrade:
		return "upgrade"
	case VersionSame:
		return "same"
	case VersionDowngrade:
		return "downgrade"
	case VersionUnknown:
		return "unknown"
	}
	klog.Warningf("unhandled versionChange value %d", v)
	return "UNHANDLED"
}

// compareVersions compares two semver versions
// if fromVersion is older than toVersion, returns versionOlder
// likewise, if fromVersion is newer, returns versionNewer
func CompareVersions(fromVersion, toVersion string) versionChange {
	if fromVersion == toVersion {
		return VersionSame
	}

	v1, err := semver.NewVersion(fromVersion)
	if err != nil {
		return VersionUnknown
	}

	v2, err := semver.NewVersion(toVersion)
	if err != nil {
		return VersionUnknown
	}

	return versionChange(v1.Compare(v2))
}

func IsVersionGreaterThanOrEqualTo(version string, major int, minor int) bool {
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
