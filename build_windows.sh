#!/bin/bash

# Build Windows executables for icmptun client and server.
# The binaries are placed in the output directory.
set -e

OUT_DIR="output"
mkdir -p "$OUT_DIR"

echo "Building Windows client..."
GOOS=windows GOARCH=amd64 go build -o "$OUT_DIR/icmptun_client.exe" ./client

echo "Building Windows server..."
GOOS=windows GOARCH=amd64 go build -o "$OUT_DIR/icmptun_server.exe" ./server

echo "Binaries built in $OUT_DIR"
