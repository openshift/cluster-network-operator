//go:build tools
// +build tools

package main

// openshift/api changed where generated CRD manifests are tracked. This import
// is now required to get the CRD manifests vendored
import (
	_ "github.com/openshift/api/cloudnetwork/v1/zz_generated.crd-manifests"
	_ "github.com/openshift/api/networkoperator/v1/zz_generated.crd-manifests"
	_ "github.com/openshift/api/operator/v1/zz_generated.crd-manifests"
	// dependency of generating CRD for install-config
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen"
)
