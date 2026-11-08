#!/bin/sh
# Verifies every file of the bundle against SHA256SUMS. Run it in the bundle
# folder after you unpack it (and check the archive's .sha256 before that).
set -eu
cd "$(dirname "$0")"
sha256sum -c SHA256SUMS
