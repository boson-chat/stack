#!/bin/sh
# Writes a self-signed TLS certificate for the fixture's server name to
# $1/cert.pem and $1/key.pem, unless both already exist. Run by each IRC
# server image at start (the key never lands in an image layer).
set -eu
dir=$1 cn=$2
[ -s "$dir/cert.pem" ] && [ -s "$dir/key.pem" ] && exit 0
mkdir -p "$dir"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 30 \
  -subj "/CN=$cn" -addext "subjectAltName=DNS:$cn,DNS:localhost,IP:127.0.0.1" \
  -keyout "$dir/key.pem" -out "$dir/cert.pem" 2>/dev/null
chmod 600 "$dir/key.pem"
