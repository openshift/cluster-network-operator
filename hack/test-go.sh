#!/bin/bash
export KUBE_FEATURE_WatchListClient=false
set -e

make test-unit
