#!/bin/bash
set -e

export KUBE_FEATURE_WatchListClient=false

make test-unit
