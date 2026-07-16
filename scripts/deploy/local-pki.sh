#!/usr/bin/env bash
set -euo pipefail

out=""
dns_names="acr.localhost,acr-api,localhost,127.0.0.1"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --out) out="${2:-}"; shift 2 ;;
    --dns) dns_names="${2:-}"; shift 2 ;;
    *) printf 'usage: local-pki.sh --out <directory> [--dns name[,name...]]\n' >&2; exit 2 ;;
  esac
done
[[ -n "$out" ]] || { printf 'usage: local-pki.sh --out <directory> [--dns name[,name...]]\n' >&2; exit 2; }
[[ "$dns_names" =~ ^[A-Za-z0-9.,:-]+$ ]] || { printf 'invalid --dns value\n' >&2; exit 2; }
mkdir -p "$out"
umask 077
openssl req -x509 -newkey rsa:2048 -nodes -days 7 -sha256 \
  -subj '/CN=ACR Local Development CA' -keyout "$out/ca.key" -out "$out/ca.crt" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj '/CN=acr.localhost' -keyout "$out/acr.key" -out "$out/acr.csr" >/dev/null 2>&1
san=""
IFS=',' read -r -a names <<< "$dns_names"
for name in "${names[@]}"; do
  [[ -n "$name" ]] || continue
  if [[ "$name" == *.*.*.* && "$name" != *[!0-9.]* ]]; then san+="${san:+,}IP:${name}"; else san+="${san:+,}DNS:${name}"; fi
done
[[ -n "$san" ]] || { printf 'at least one DNS name is required\n' >&2; exit 2; }
printf 'subjectAltName=%s\n' "$san" > "$out/acr.ext"
openssl x509 -req -days 7 -sha256 -in "$out/acr.csr" -CA "$out/ca.crt" -CAkey "$out/ca.key" -CAcreateserial \
  -extfile "$out/acr.ext" -out "$out/acr.crt" >/dev/null 2>&1
rm -f "$out/acr.csr" "$out/acr.ext" "$out/ca.srl"
