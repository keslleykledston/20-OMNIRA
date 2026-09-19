#!/bin/bash
set -e

ROOT_DIR="/data/home-moved/Projects/_legacy_lowercase_projects/20-OMNIRA"
cd "$ROOT_DIR"

echo "🚀 OMNIRA - Iniciando serviços"
echo ""

# Start Docker services (PostgreSQL, NATS, OTel)
echo "📦 Iniciando serviços Docker (PostgreSQL, NATS, OTel)..."
docker-compose up -d postgres nats otel-collector 2>/dev/null || true
sleep 5

# Build and run API
echo "🔨 Compilando backend API (Go)..."
go build -o bin/omnira-api cmd/api/main.go

echo "🚀 Iniciando API (localhost:8080)..."
./bin/omnira-api > logs/api.log 2>&1 &
API_PID=$!
echo "   PID: $API_PID"
sleep 3

# Build and run Frontend
echo "🔨 Instalando dependências frontend..."
cd web
npm ci > /dev/null 2>&1

echo "🚀 Iniciando Frontend (localhost:3000)..."
npm run dev > ../logs/web.log 2>&1 &
WEB_PID=$!
echo "   PID: $WEB_PID"
sleep 3

# Status
echo ""
echo "✅ Serviços iniciados:"
echo ""
echo "   Frontend: http://localhost:3000"
echo "   API:      http://localhost:8080"
echo "   Nginx:    https://omnira.devops.k3gsolutions.com.br"
echo ""
echo "📋 Logs:"
echo "   API:      tail -f logs/api.log"
echo "   Web:      tail -f logs/web.log"
echo "   Nginx:    sudo tail -f /var/log/nginx/access.log"
echo ""
echo "⏹️  Para parar:"
echo "   kill $API_PID $WEB_PID"
echo ""

wait
