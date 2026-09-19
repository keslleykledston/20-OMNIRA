#!/bin/bash

# validate-specs.sh — valida OpenAPI e AsyncAPI specs

set -euo pipefail

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

ERRORS=0

validate_yaml() {
    local file="$1"
    local type="$2"

    echo -n "Validating $type ($file)... "

    if ! python3 -c "import yaml; yaml.safe_load(open('$file'))" 2>/dev/null; then
        echo -e "${RED}FAILED${NC}"
        echo "  Error: Invalid YAML syntax"
        ((ERRORS++))
        return 1
    fi

    echo -e "${GREEN}OK${NC}"
    return 0
}

echo -e "${YELLOW}=== API Specification Validation ===${NC}"
echo ""

# Validate OpenAPI
if [ -f "docs/api/openapi.yaml" ]; then
    validate_yaml "docs/api/openapi.yaml" "OpenAPI 3.0.0"
else
    echo -e "${RED}✗ docs/api/openapi.yaml not found${NC}"
    ((ERRORS++))
fi

# Validate AsyncAPI
if [ -f "docs/async/asyncapi.yaml" ]; then
    validate_yaml "docs/async/asyncapi.yaml" "AsyncAPI 3.0.0"
else
    echo -e "${RED}✗ docs/async/asyncapi.yaml not found${NC}"
    ((ERRORS++))
fi

# Validate documentation
echo -n "Checking docs/API.md... "
if [ -f "docs/API.md" ] && grep -q "OpenAPI" "docs/API.md"; then
    echo -e "${GREEN}OK${NC}"
else
    echo -e "${RED}MISSING${NC}"
    ((ERRORS++))
fi

echo -n "Checking docs/EVENTS.md... "
if [ -f "docs/EVENTS.md" ] && grep -q "AsyncAPI" "docs/EVENTS.md"; then
    echo -e "${GREEN}OK${NC}"
else
    echo -e "${RED}MISSING${NC}"
    ((ERRORS++))
fi

echo ""

if [ $ERRORS -eq 0 ]; then
    echo -e "${GREEN}✓ All specifications valid${NC}"
    exit 0
else
    echo -e "${RED}✗ $ERRORS error(s) found${NC}"
    exit 1
fi
