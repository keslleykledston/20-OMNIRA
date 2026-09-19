#!/bin/bash

# validate-helm.sh — valida chart Helm

set -euo pipefail

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

CHART_DIR="./helm/omnira"
ERRORS=0

echo -e "${YELLOW}=== Helm Chart Validation ===${NC}"
echo ""

# Check if helm is installed
if ! command -v helm &> /dev/null; then
    echo -e "${RED}✗ helm not found. Install: https://helm.sh/docs/intro/install/${NC}"
    exit 1
fi

echo "Helm version: $(helm version --short)"
echo ""

# Lint chart
echo -n "Linting chart... "
if helm lint "$CHART_DIR" > /dev/null 2>&1; then
    echo -e "${GREEN}OK${NC}"
else
    echo -e "${RED}FAILED${NC}"
    helm lint "$CHART_DIR"
    ((ERRORS++))
fi

# Template rendering
echo -n "Testing template rendering... "
if helm template omnira "$CHART_DIR" > /dev/null 2>&1; then
    echo -e "${GREEN}OK${NC}"
else
    echo -e "${RED}FAILED${NC}"
    helm template omnira "$CHART_DIR"
    ((ERRORS++))
fi

# Validate YAML
echo -n "Validating YAML... "
if helm template omnira "$CHART_DIR" | python3 -c "import sys, yaml; [yaml.safe_load(doc) for doc in sys.stdin.read().split('---')]" 2>/dev/null; then
    echo -e "${GREEN}OK${NC}"
else
    echo -e "${RED}FAILED${NC}"
    ((ERRORS++))
fi

# Check required fields
echo -n "Checking Chart.yaml... "
if [ -f "$CHART_DIR/Chart.yaml" ] && grep -q "name: omnira" "$CHART_DIR/Chart.yaml"; then
    echo -e "${GREEN}OK${NC}"
else
    echo -e "${RED}MISSING${NC}"
    ((ERRORS++))
fi

# Check values.yaml
echo -n "Checking values.yaml... "
if [ -f "$CHART_DIR/values.yaml" ]; then
    echo -e "${GREEN}OK${NC}"
else
    echo -e "${RED}MISSING${NC}"
    ((ERRORS++))
fi

# Check templates exist
echo -n "Checking templates... "
TEMPLATE_COUNT=$(find "$CHART_DIR/templates" -type f -name "*.yaml" 2>/dev/null | wc -l)
if [ "$TEMPLATE_COUNT" -gt 0 ]; then
    echo -e "${GREEN}OK (${TEMPLATE_COUNT} templates)${NC}"
else
    echo -e "${RED}NO TEMPLATES FOUND${NC}"
    ((ERRORS++))
fi

echo ""

if [ $ERRORS -eq 0 ]; then
    echo -e "${GREEN}✓ Chart validation passed${NC}"
    echo ""
    echo "Quick start:"
    echo "  helm install omnira $CHART_DIR --namespace omnira --create-namespace"
    exit 0
else
    echo -e "${RED}✗ $ERRORS error(s) found${NC}"
    exit 1
fi
