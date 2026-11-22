#!/usr/bin/env bash
set -euo pipefail

# REQUIRED: Stops Git Bash from converting /C=IN/... to a Windows path
export MSYS_NO_PATHCONV=1

# Go to the script's directory and use relative paths (avoids spaces-in-path bugs)
cd "$(dirname "$0")"
mkdir -p .

echo "Generating self-signed certificate in $(pwd)..."

openssl req -x509 \
  -nodes \
  -days 365 \
  -newkey rsa:2048 \
  -keyout key.pem \
  -out cert.pem \
  -subj "/C=IN/ST=Local/L=Local/O=HITL-Platform/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"

echo ""
echo "✓ Generated:"
echo "    cert: $(pwd)/cert.pem"
echo "    key:  $(pwd)/key.pem"