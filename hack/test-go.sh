#!/bin/bash
export KUBE_FEATURE_AtomicFIFO=false
export KUBE_FEATURE_WatchListClient=false
set -e

make test-unit
