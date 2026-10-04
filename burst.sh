#!/usr/bin/env bash
set -e
# Arguments:
# $1: Base URL (default: http://localhost:8080)
# $2: Concurrency count (default: 500)
# $3: Target seat number (default: A12)
BASE_URL="${1:-http://localhost:8080}"
CONCURRENCY="${2:-500}"
TARGET_SEAT="${3:-A12}"
echo "=============================================================="
echo "🎯 Executing Concurrency Burst against: ${BASE_URL}"
echo "👥 Concurrency level : ${CONCURRENCY} requests"
echo "💺 Target Hot Seat   : ${TARGET_SEAT}"
echo "=============================================================="
# 1. If running inside Docker container where /app/burst is pre-compiled
if [ -f "/app/burst" ]; then
    /app/burst "${BASE_URL}" "${CONCURRENCY}" "${TARGET_SEAT}"
# 2. If a local pre-compiled binary exists in current directory
elif [ -f "./burst" ]; then
    ./burst "${BASE_URL}" "${CONCURRENCY}" "${TARGET_SEAT}"
# 3. Otherwise, use Go toolchain to run
elif command -v go >/dev/null 2>&1; then
    go run cmd/burst/main.go "${BASE_URL}" "${CONCURRENCY}" "${TARGET_SEAT}"
else
    echo "❌ Error: Neither Go compiler nor pre-compiled burst binary was found."
    exit 1
fi