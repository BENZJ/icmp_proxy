#!/bin/bash

# This script builds and runs the ICMP server.
# It requires root privileges to run due to raw socket usage.

# Exit immediately if a command exits with a non-zero status.
set -e

echo "Building the server..."
# The -o flag places the output binary in the project root for convenience.
go build -o icmptun_server ./server

echo "Build successful. Running server with sudo..."
echo "You may be prompted for your password."

# Run the server with sudo.
sudo ./icmptun_server

# Clean up the binary after the server is stopped (e.g., with Ctrl+C)
echo "Server stopped. Cleaning up..."
rm ./icmptun_server
