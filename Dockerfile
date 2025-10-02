FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.24-openshift-4.21 AS builder
WORKDIR /go/src/github.com/openshift/cluster-network-operator
COPY . .
RUN hack/build-go.sh

FROM registry.ci.openshift.org/ocp/4.21:base-rhel9
COPY --from=builder  /go/src/github.com/openshift/cluster-network-operator/cluster-network-operator /usr/bin/
COPY --from=builder  /go/src/github.com/openshift/cluster-network-operator/cluster-network-check-endpoints /usr/bin/
COPY --from=builder  /go/src/github.com/openshift/cluster-network-operator/cluster-network-check-target /usr/bin/

COPY manifests /manifests
COPY bindata /bindata
ENV OPERATOR_NAME=cluster-network-operator
CMD ["/usr/bin/cluster-network-operator"]
LABEL io.openshift.release.operator true
