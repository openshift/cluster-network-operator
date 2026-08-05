FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.26-openshift-5.0 AS builder
WORKDIR /go/src/github.com/openshift/cluster-network-operator
COPY . .
RUN go mod vendor && hack/build-go.sh && cd test && ./hack/build-tests-ext.sh && gzip -9 bin/cluster-network-operator-tests-ext

FROM registry.ci.openshift.org/ocp/5.0:base-rhel9
COPY --from=builder  /go/src/github.com/openshift/cluster-network-operator/cluster-network-operator /usr/bin/
COPY --from=builder  /go/src/github.com/openshift/cluster-network-operator/cluster-network-check-endpoints /usr/bin/
COPY --from=builder  /go/src/github.com/openshift/cluster-network-operator/cluster-network-check-target /usr/bin/
COPY --from=builder  /go/src/github.com/openshift/cluster-network-operator/test/bin/cluster-network-operator-tests-ext.gz /usr/bin/cluster-network-operator-tests-ext.gz

COPY manifests /manifests
COPY bindata /bindata
ENV OPERATOR_NAME=cluster-network-operator
CMD ["/usr/bin/cluster-network-operator"]
LABEL io.openshift.release.operator true
