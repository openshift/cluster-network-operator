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
