FROM registry.svc.ci.openshift.org/openshift/release:golang-1.14 AS builder
WORKDIR /go/src/github.com/openshift/cluster-network-operator
COPY . .
RUN hack/build-go.sh; \
    mkdir -p /tmp/build; \
    cp /go/src/github.com/openshift/cluster-network-operator/_output/linux/$(go env GOARCH)/cluster-network-operator /tmp/build/; \
    cp /go/src/github.com/openshift/cluster-network-operator/_output/linux/$(go env GOARCH)/cluster-network-renderer /tmp/build/

FROM registry.svc.ci.openshift.org/openshift/origin-v4.0:base
COPY --from=builder /tmp/build/cluster-network-operator /usr/bin/
COPY --from=builder /tmp/build/cluster-network-renderer /usr/bin/
COPY manifests /manifests
COPY bindata /bindata
ENV OPERATOR_NAME=cluster-network-operator
CMD ["/usr/bin/cluster-network-operator"]
LABEL io.openshift.release.operator true
