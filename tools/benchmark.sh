#!/bin/bash

# benchmark.sh — Benchmark OMNIRA API performance

set -euo pipefail

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

BASE_URL="${BASE_URL:-http://localhost:8080}"
RESULTS_DIR="./benchmark-results"

mkdir -p "$RESULTS_DIR"

echo -e "${YELLOW}=== OMNIRA Performance Benchmark ===${NC}"
echo ""

# Check API availability
echo -n "Checking API availability... "
if curl -s "$BASE_URL/internal/health/live" > /dev/null; then
    echo -e "${GREEN}OK${NC}"
else
    echo -e "${RED}FAILED${NC}"
    echo "API not available at $BASE_URL"
    exit 1
fi

echo ""

# 1. Health endpoint baseline
echo -e "${YELLOW}[1/5] Health Endpoint Baseline${NC}"
echo "Running 100 requests to /healthz..."

HEALTH_RESULTS="$RESULTS_DIR/health-baseline.txt"
ab -n 100 -c 1 -q "$BASE_URL/healthz" 2>&1 | tee "$HEALTH_RESULTS" | grep -E "Requests per second|Time per request|Failed requests"

echo ""

# 2. Metrics endpoint baseline
echo -e "${YELLOW}[2/5] Metrics Endpoint Baseline${NC}"
echo "Running 100 requests to /metrics..."

METRICS_RESULTS="$RESULTS_DIR/metrics-baseline.txt"
ab -n 100 -c 1 -q "$BASE_URL/metrics" 2>&1 | tee "$METRICS_RESULTS" | grep -E "Requests per second|Time per request|Failed requests"

echo ""

# 3. Concurrent health checks
echo -e "${YELLOW}[3/5] Concurrent Health Checks (10 concurrent)${NC}"
echo "Running 500 requests with 10 concurrent connections..."

HEALTH_CONCURRENT="$RESULTS_DIR/health-concurrent.txt"
ab -n 500 -c 10 -q "$BASE_URL/healthz" 2>&1 | tee "$HEALTH_CONCURRENT" | grep -E "Requests per second|Time per request|Failed requests"

echo ""

# 4. Internal health/live (Kubernetes liveness)
echo -e "${YELLOW}[4/5] Internal Health Endpoints (Kubernetes probes)${NC}"

echo "Testing /internal/health/live..."
LIVE_START=$(date +%s%N)
curl -s "$BASE_URL/internal/health/live" > /dev/null
LIVE_END=$(date +%s%N)
LIVE_TIME=$(( (LIVE_END - LIVE_START) / 1000000 ))
echo "Response time: ${LIVE_TIME}ms"

echo "Testing /internal/health/ready..."
READY_START=$(date +%s%N)
curl -s "$BASE_URL/internal/health/ready" > /dev/null
READY_END=$(date +%s%N)
READY_TIME=$(( (READY_END - READY_START) / 1000000 ))
echo "Response time: ${READY_TIME}ms"

echo ""

# 5. Resource usage baseline
echo -e "${YELLOW}[5/5] Resource Usage Baseline${NC}"

# Check if running in Docker/container
if [ -f "/.dockerenv" ] || [ -f "/run/.containerenv" ]; then
    echo "Running in container"
    if [ -f "/sys/fs/cgroup/memory/memory.usage_in_bytes" ]; then
        MEM_KB=$(cat /sys/fs/cgroup/memory/memory.usage_in_bytes 2>/dev/null || echo "0")
        MEM_MB=$((MEM_KB / 1024 / 1024))
        echo "Memory usage: ${MEM_MB}MB"
    fi
else
    echo "Local process info:"
    ps aux | grep omnira-api | grep -v grep || echo "(API not running as process)"
fi

echo ""

# Summary
echo -e "${YELLOW}=== Benchmark Summary ===${NC}"
echo "Results saved to: $RESULTS_DIR/"
echo ""
echo "Key metrics to monitor:"
echo "  - Requests per second (RPS)"
echo "  - P95/P99 latency"
echo "  - Error rate"
echo "  - Memory usage"
echo ""
echo "To run full load test with k6:"
echo "  k6 run tools/k6/health-check.js -e BASE_URL=$BASE_URL"

