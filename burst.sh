#!/usr/bin/env bash
set -e
# Default to http://localhost:8080 if no argument is provided
BASE_URL="${1:-http://localhost:8080}"
echo "=============================================================="
echo "🎯 Executing Concurrency Burst against: ${BASE_URL}"
echo "=============================================================="
# 1. If running inside Docker container where /app/burst binary is pre-compiled
if [ -f "/app/burst" ]; then
    /app/burst "${BASE_URL}"
# 2. If a local pre-compiled binary exists in current directory
elif [ -f "./burst" ]; then
    ./burst "${BASE_URL}"
# 3. Otherwise, use Go toolchain to run
elif command -v go >/dev/null 2>&1; then
    go run cmd/burst/main.go "${BASE_URL}"
else
    echo "❌ Error: Neither Go compiler nor pre-compiled burst binary was found."
    exit 1
fi