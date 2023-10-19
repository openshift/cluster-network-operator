FROM registry.ci.openshift.org/ocp/builder:rhel-8-golang-1.20-openshift-4.14 AS builder
WORKDIR /go/src/github.com/openshift/cluster-network-operator
COPY . .
RUN hack/build-go.sh

FROM registry.ci.openshift.org/ocp/4.14:base
COPY --from=builder  /go/src/github.com/openshift/cluster-network-operator/cluster-network-operator /usr/bin/
COPY --from=builder  /go/src/github.com/openshift/cluster-network-operator/cluster-network-check-endpoints /usr/bin/
COPY --from=builder  /go/src/github.com/openshift/cluster-network-operator/cluster-network-check-target /usr/bin/

COPY manifests /manifests
RUN mv /manifests/0000_70_cluster-network-operator_01_crd.yaml /manifests/0000_50_cluster-network-operator_01_crd.yaml
COPY bindata /bindata
ENV OPERATOR_NAME=cluster-network-operator
CMD ["/usr/bin/cluster-network-operator"]
LABEL io.openshift.release.operator true
