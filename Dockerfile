FROM golang:1.10.3 AS build-env

COPY . /go/src/github.com/openshift/cluster-network-operator
WORKDIR /go/src/github.com/openshift/cluster-network-operator
RUN ./hack/build-go.sh

FROM scratch
COPY --from=build-env /go/src/github.com/openshift/cluster-network-operator/_output/linux/amd64/cluster-network-operator /bin/cluster-network-operator
COPY --from=build-env /go/src/github.com/openshift/cluster-network-operator/_output/linux/amd64/cluster-network-renderer /bin/cluster-network-renderer
COPY manifests /manifests

ENV OPERATOR_NAME=cluster-network-operator
CMD ["/bin/cluster-network-operator"]
