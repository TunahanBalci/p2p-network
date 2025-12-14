#!/bin/bash

# Check if Go is installed
if ! command -v go &> /dev/null; then
    echo "Error: Go is not installed or not in your PATH."
    echo "Please install Go from https://go.dev/dl/ and try again."
    exit 1
fi

echo "Go found. Building application..."

# Initialize/Tidy module
echo "Running go mod tidy..."
go mod tidy

# Build
echo "Building binary..."
if go build -o p2p-chat .; then
    echo "Build successful! Run ./p2p-chat to start."
else
    echo "Build failed."
    exit 1
fi
