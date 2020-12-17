FROM registry.svc.ci.openshift.org/openshift/release:golang-1.15 AS builder
WORKDIR /go/src/github.com/openshift/cluster-network-operator
COPY . .
RUN hack/build-go.sh

FROM registry.svc.ci.openshift.org/openshift/origin-v4.0:base
COPY --from=builder  /go/src/github.com/openshift/cluster-network-operator/cluster-network-operator /usr/bin/
COPY --from=builder  /go/src/github.com/openshift/cluster-network-operator/cluster-network-check-endpoints /usr/bin/

COPY manifests /manifests
COPY vendor/github.com/openshift/api/operatorcontrolplane/v1alpha1/0000_10-pod-network-connectivity-check.crd.yaml /manifests
COPY bindata /bindata
ENV OPERATOR_NAME=cluster-network-operator
CMD ["/usr/bin/cluster-network-operator"]
LABEL io.openshift.release.operator true
