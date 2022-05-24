#!/usr/bin/env bash
set -e
if [ -t 0 ]; then echo 'Please run "make check" instead'; echo; fi
make verify