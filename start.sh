#!/bin/bash
echo "Starting CI/CD System..."

echo
echo "[1/3] Installing Node.js dependencies..."
cd nodejs-service
[ ! -d "node_modules" ] && npm install
npm start &
NODE_PID=$!
cd ..

sleep 2

echo
echo "[2/3] Starting Go Builder Service..."
cd go-builder
[ ! -f "go.sum" ] && go mod tidy
go run main.go &
GO_PID=$!
cd ..

sleep 2

echo
echo "[3/3] All services started!"
echo
echo "Node.js Service: http://localhost:3000"
echo "Go Builder:      http://localhost:8080"
echo
echo "Press Ctrl+C to stop all services..."

trap "kill $NODE_PID $GO_PID 2>/dev/null; exit" INT TERM
wait
