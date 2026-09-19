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
if [ -f "contracts/openapi/omnira-v1.yaml" ]; then
    validate_yaml "contracts/openapi/omnira-v1.yaml" "OpenAPI 3.0.0"
else
    echo -e "${RED}✗ contracts/openapi/omnira-v1.yaml not found${NC}"
    ((ERRORS++))
fi

# Semantic OpenAPI lint (needs npx + network the first time)
if command -v npx >/dev/null 2>&1; then
    echo -n "Linting OpenAPI (redocly)... "
    if npx --yes @redocly/cli@latest lint contracts/openapi/omnira-v1.yaml >/tmp/redocly.out 2>&1; then
        echo -e "${GREEN}OK${NC}"
    else
        echo -e "${RED}FAILED${NC}"; tail -20 /tmp/redocly.out; ((ERRORS++))
    fi
fi

# Validate AsyncAPI
if [ -f "contracts/asyncapi/omnira-v1.yaml" ]; then
    validate_yaml "contracts/asyncapi/omnira-v1.yaml" "AsyncAPI 2.6"
else
    echo -e "${RED}✗ contracts/asyncapi/omnira-v1.yaml not found${NC}"
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
