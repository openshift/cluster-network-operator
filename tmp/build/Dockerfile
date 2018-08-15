FROM alpine:3.6

RUN adduser -D openshift-network-operator
USER openshift-network-operator

ADD tmp/_output/bin/openshift-network-operator /usr/local/bin/openshift-network-operator
