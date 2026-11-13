#!/bin/bash
set -e

echo "Running TLS Profile Compliance test..."
echo "This test is disruptive and may take 2-4 hours to complete."
echo ""

cd "$(dirname "$0")"

./bin/cluster-network-operator-tests-ext run-test \
  -n "TLS Profile Compliance" \
  --timeout 4h \
  2>&1 | tee "tls-test-$(date +%Y%m%d-%H%M%S).log"
