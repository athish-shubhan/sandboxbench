#!/bin/sh
# runc standing in for the runsc arm, for smoke tests of the harness on
# machines without gVisor (CI): drops runsc-only global flags and runs runc.
# Numbers measured this way are runc's, not gVisor's.
for a in "$@"; do
  case "$a" in --platform=*|--network=*) ;; *) set -- "$@" "$a" ;; esac
  shift
done
exec "${RUNC:-runc}" "$@"
