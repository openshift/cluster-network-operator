package network

import (
	"strconv"
	"testing"

	. "github.com/onsi/gomega"
)

func TestDirection(t *testing.T) {
	for idx, tc := range []struct {
		from   string
		to     string
		result versionChange
	}{
		{
			"1.2.3",
			"1.2.4",
			versionUpgrade,
		},
		{
			"1.2.4",
			"1.2.3",
			versionDowngrade,
		},
		{
			"asdf",
			"fdsa",
			versionUnknown,
		},
		{
			"1.1.1",
			"1.1.1",
			versionSame,
		},
		{
			"4.7.0-0.ci-2021-01-16-102811",
			"4.7.0-0.ci-2021-01-18-121038",
			versionUpgrade,
		},
		{
			"4.7.0-0.ci-2021-01-18-121038",
			"4.7.0-0.ci-2021-01-16-102811",
			versionDowngrade,
		},
		{
			"4.6.0-0.ci-2021-01-18-121038",
			"4.7.0-0.ci-2021-01-16-102811",
			versionUpgrade,
		},
		{
			"4.6.5",
			"4.7.0-0.ci-2021-01-16-102811",
			versionUpgrade,
		},
	} {
		t.Run(strconv.Itoa(idx), func(t *testing.T) {
			g := NewGomegaWithT(t)
			g.Expect(compareVersions(tc.from, tc.to)).To(Equal(tc.result))
		})
	}
}

func TestVersionComparison(t *testing.T) {
	for idx, tc := range []struct {
		version                    string
		otherVersionMajor          int
		otherVersionMinor          int
		resultLessThanOrEqualTo    bool
		resultGreaterThanOrEqualTo bool
	}{
		{
			"4.14",
			4, 14,
			true, // <=
			true, // >=
		},
		{
			"4.14",
			4, 15,
			true,  // <=
			false, // >=
		},
		{
			"4.14",
			4, 13,
			false, // <=
			true,  // >=
		},
		{
			"4.14.0-0.ci.test-2023-06-14-124931-ci-ln-md7ivqb-latest",
			4, 14,
			true, // <=
			true, // >=
		},
		{
			"4.14.0-0.ci.test-2023-06-14-124931-ci-ln-md7ivqb-latest",
			4, 15,
			true,  // <=
			false, // >=
		},
		{
			"4.14.0-0.ci.test-2023-06-14-124931-ci-ln-md7ivqb-latest",
			4, 13,
			false, // <=
			true,  // >=
		},
	} {
		t.Run(strconv.Itoa(idx), func(t *testing.T) {
			g := NewGomegaWithT(t)

			g.Expect(isVersionLessThanOrEqualTo(tc.version, tc.otherVersionMajor, tc.otherVersionMinor)).To(Equal(tc.resultLessThanOrEqualTo))
			g.Expect(isVersionGreaterThanOrEqualTo(tc.version, tc.otherVersionMajor, tc.otherVersionMinor)).To(Equal(tc.resultGreaterThanOrEqualTo))
		})
	}
}
