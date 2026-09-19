#!/bin/bash
set -e

echo "🚀 OMNIRA Deployment Script"
echo ""

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# Check Docker
if ! command -v docker &> /dev/null; then
    echo -e "${RED}❌ Docker não encontrado${NC}"
    exit 1
fi

# Check Docker Compose
if ! command -v docker-compose &> /dev/null; then
    echo -e "${RED}❌ Docker Compose não encontrado${NC}"
    exit 1
fi

# Build frontend
echo -e "${YELLOW}📦 Building frontend...${NC}"
cd web
npm ci
npm run build
cd ..

# Build backend
echo -e "${YELLOW}📦 Building backend...${NC}"
go mod tidy
go build -o bin/omnira-api cmd/api/main.go

# Start containers
echo -e "${YELLOW}🐳 Starting Docker containers...${NC}"
docker-compose down || true
docker-compose build
docker-compose up -d

# Wait for health
echo -e "${YELLOW}⏳ Waiting for services to be healthy...${NC}"
for i in {1..30}; do
    if docker-compose ps | grep -q "healthy"; then
        echo -e "${GREEN}✅ Services are healthy${NC}"
        break
    fi
    sleep 2
done

# Show status
echo ""
echo -e "${GREEN}✅ Deployment Complete!${NC}"
echo ""
echo "📍 Frontend: http://omnira.devops.k3gsolutions.com.br"
echo "📍 API: http://omnira.devops.k3gsolutions.com.br/api"
echo "📍 Docs: http://omnira.devops.k3gsolutions.com.br/api/docs"
echo ""
echo "🔍 Service Status:"
docker-compose ps
echo ""
echo "📋 Logs:"
echo "  docker logs omnira-nginx"
echo "  docker logs omnira-api"
echo "  docker logs omnira-web"
echo "  docker logs omnira-postgres"
