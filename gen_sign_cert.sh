#!/bin/bash

if [ -z "$1" ]; then
	echo "Usage: $0 <common name>"
	exit
fi

cn=$1
echo "Generating cert for common name = " ${cn}
openssl genrsa -out test.key 2048

openssl req -new -text \
          -extensions v3_req \
          -addext "subjectAltName = DNS:${cn}" \
          -subj "/C=US/O=ovnkubernetes/OU=kind/CN=${cn}" \
          -key test.key \
          -out test.csr

csr_64=$(cat test.csr | base64 | tr -d "\n")

cat <<EOF | oc apply -f -
apiVersion: certificates.k8s.io/v1
kind: CertificateSigningRequest
metadata:
  name: test
spec:
  groups:
  - system:authenticated
  request: ${csr_64}
  signerName: network.openshift.io/ipsec
  usages:
  - client auth
EOF

cert_64=$(oc get csr/test -o jsonpath='{.status.certificate}')

oc get csr/test -o jsonpath='{.status.certificate}' | base64 -d | openssl x509 -outform pem -text

oc delete csr/test
