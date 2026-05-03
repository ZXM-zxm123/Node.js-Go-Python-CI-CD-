@echo off
echo Starting CI/CD System...

echo.
echo [1/3] Installing Node.js dependencies...
cd nodejs-service
if not exist node_modules call npm install
start "Node.js Service" cmd /k "npm start"
cd ..

timeout /t 2 /nobreak >nul

echo.
echo [2/3] Starting Go Builder Service...
cd go-builder
if not exist go.sum call go mod tidy
start "Go Builder" cmd /k "go run main.go"
cd ..

timeout /t 2 /nobreak >nul

echo.
echo [3/3] All services started!
echo.
echo Node.js Service: http://localhost:3000
echo Go Builder:      http://localhost:8080
echo.
echo Press any key to stop all services...
pause >nul

echo.
echo Stopping services...
taskkill /FI "WINDOWTITLE eq Node.js Service*" /F >nul 2>&1
taskkill /FI "WINDOWTITLE eq Go Builder*" /F >nul 2>&1
echo Done!
