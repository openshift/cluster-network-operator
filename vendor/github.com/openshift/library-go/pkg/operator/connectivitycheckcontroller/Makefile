# sync the podnetworkconnectivitychecks crd from github.com/openshift/api for embedding

CRD_GROUP=controlplane.operator.openshift.io
CRD_RESOURCE=podnetworkconnectivitychecks
CRD_TARGET=manifests/$(CRD_GROUP)_$(CRD_RESOURCE).yaml
CRD_SOURCE_DIR=$(shell go list -f {{.Dir}} github.com/openshift/api/operatorcontrolplane/v1alpha1)
CRD_SOURCE=$(CRD_SOURCE_DIR)/0000_10-pod-network-connectivity-check.crd.yaml

.PHONY: update
update: $(CRD_TARGET)

$(CRD_TARGET): $(CRD_SOURCE) FORCE
	mkdir -p $(dir $@)
	cp $< $@

.PHONY: FORCE
FORCE:

verify:
	diff -Naup $(CRD_TARGET) $(CRD_SOURCE)

