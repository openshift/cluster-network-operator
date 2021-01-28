package network

import "github.com/Masterminds/semver"

const (
	versionUpgrade   = -1
	versionSame      = 0
	versionDowngrade = 1
	versionUnknown   = 2
)

// compareVersions compares two semver versions
// if fromVersion is older than toVersion, returns versionOlder
// likewise, if fromVersion is newer, returns versionNewer
func compareVersions(fromVersion, toVersion string) int {
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

	return v1.Compare(v2)
}
