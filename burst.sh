#!/usr/bin/env bash
set -e

BASE_URL="${1:-http://localhost:8080}"
echo "Running Concurrency Burst against ${BASE_URL}..."
go run cmd/burst/main.go "${BASE_URL}"