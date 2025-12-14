@echo off
where go >nul 2>nul
if %errorlevel% neq 0 (
    echo Error: Go is not installed or not in your PATH.
    echo Please install Go from https://go.dev/dl/ and try again.
    pause
    exit /b 1
)

echo Go found. Building application...

echo Running go mod tidy...
go mod tidy

echo Building binary...
go build -o p2p-chat.exe .
if %errorlevel% neq 0 (
    echo Build failed.
    pause
    exit /b 1
)

echo Build successful! Run p2p-chat.exe to start.
pause
