#!/usr/bin/env bash
set -euo pipefail

fingerprint="9DCD0E7D385C8247E2F5E7FC2C43EBC02D8C8781"
output="signing/release-tag-signing-key.asc"

gpg --batch --with-colons --list-keys "$fingerprint" | awk -F: '$1 == "fpr" { print $10; exit }' | grep -Fx "$fingerprint" >/dev/null
mkdir -p signing
gpg --batch --armor --export "$fingerprint" > "$output"
