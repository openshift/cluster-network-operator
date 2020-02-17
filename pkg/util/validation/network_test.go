package validation

import (
	"testing"

	. "github.com/onsi/gomega"
)

// URI validates uri as being a valid http(s) url and returns the url scheme.
func TestURI(t *testing.T) {
	g := NewGomegaWithT(t)
	validHTTPURIs := []string{
		"http://1.2.3.4",
		"http://1.2.3.4/",
		"http://1.2.3.4:80",
		"http://1.2.3.4:80/",
		"http://redhat",
		"http://red_hat.com",
		"http://redhat.com",
		"http://REDHAT.COM",
		"http://RedHat.com",
		"http://redhat.com/",
		"http://redhat.com:80",
		"http://redhat.com:80/",
		"http://-8080:8080/",
		"http://日©ñعசிש.com",
	}
	validHTTPSURIs := []string{
		"https://1.2.3.4",
		"https://EXAMPLe.com:8080/",
	}
	invalidURIs := []string{
		"http://1.2.3.4:8080808080",
		"redhat.com",
	}
	for _, uri := range validHTTPURIs {
		_, err := URI(uri)
		g.Expect(err).NotTo(HaveOccurred())
	}
	for _, uri := range validHTTPSURIs {
		_, err := URI(uri)
		g.Expect(err).NotTo(HaveOccurred())
	}
	for _, uri := range invalidURIs {
		_, err := URI(uri)
		g.Expect(err).To(HaveOccurred())
	}

}
