#!/usr/bin/env bash

set -e

ARG="$1"

ALL_ARCHS=(
    "linux/amd64" 
    "linux/arm64" 
    "linux/riscv64" 
)

cd "$(dirname "$0")/.."

if [[ "$ARG" == "--help" || "$ARG" == "-h" || "$ARG" == "help" ]]; then
    echo "Usage: ./addons/build.sh [cross|all|help]"
    echo ""
    echo "Options:"
    echo "  (none)      Build for the current architecture only"
    echo "  cross, all  Cross-compile for all supported architectures"
    echo "  -h, --help  Show this help message"
    echo ""
    echo "Supported cross-compilation architectures:"
    for arch in "${ALL_ARCHS[@]}"; do
        echo "  - $arch"
    done
    exit 0
fi

VERSION_FILE="VERSION"

if [ -f "${VERSION_FILE}" ]; then
    VERSION="$(head -1 "${VERSION_FILE}" | tr -d '[:space:]')"
else
    echo "Error: VERSION file not found"
    exit 1
fi

# Cross-compile for different architectures

if [ "$ARG" == "cross" ] || [ "$ARG" == "all" ]; then
    mkdir -p dist
    for arch in "${ALL_ARCHS[@]}"; do
        # split string by / into os and arch
        os=$(echo "$arch" | cut -d'/' -f1)
        arch=$(echo "$arch" | cut -d'/' -f2)
        echo "Building for $os/$arch..."
        CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build \
            -trimpath \
            -ldflags="-s -w" \
            -buildvcs=false \
            -o "dist/tui-chart-$os-$VERSION-$arch" \
            ./cmd/tui-chart/
    done
else
    echo "Building tui-chart for current architecture..."
    CGO_ENABLED=0 go build \
        -trimpath \
        -ldflags="-s -w" \
        -buildvcs=false \
        -o tui-chart \
        ./cmd/tui-chart/
fi

echo "Build done!"
