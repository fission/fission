#!/bin/bash
set -e

VERSION="v2.0.0-local"
DIST_DIR="./dist-local"

echo "Building all Fission binaries..."
mkdir -p $DIST_DIR

# Build binary
GO_BIN=$HOME/.go_local/go/bin/go

# Build fission-bundle
echo "  - Building fission-bundle..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $GO_BIN build -o $DIST_DIR/fission-bundle ./cmd/fission-bundle

# Build fetcher
echo "  - Building fetcher..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $GO_BIN build -o $DIST_DIR/fetcher ./cmd/fetcher

# Build pre-upgrade-checks
echo "  - Building pre-upgrade-checks..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $GO_BIN build -o $DIST_DIR/pre-upgrade-checks ./cmd/preupgradechecks

# Build reporter
echo "  - Building reporter..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $GO_BIN build -o $DIST_DIR/reporter ./cmd/reporter


# Build reporter
echo "  - Building CLI..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dist/fission-cli_linux_amd64_v1/fission ./cmd/fission-cli


echo "Preparing Helm Chart..."
# Remove old chart copy if exists to prevent nesting
rm -rf $DIST_DIR/fission-all
# Create a copy of the chart
cp -r charts/fission-all $DIST_DIR/fission-all
# Ensure the watch-all-namespaces feature is enabled (no-op when already the default)
sed -i "s/watchAllNamespaces: false/watchAllNamespaces: true/" $DIST_DIR/fission-all/values.yaml

echo "Packaging Helm Chart..."
helm package $DIST_DIR/fission-all -d $DIST_DIR --version 2.0.0-local --app-version 2.0.0-local

TIMESTAMP=$(date +%s)
TAG="local-$TIMESTAMP"

echo "Building Docker images with tag $TAG..."

# Build fission-bundle
docker build -t ghcr.io/fission/fission-bundle:$TAG -f cmd/fission-bundle/Dockerfile --build-arg TARGETPLATFORM=dist-local .
minikube image load ghcr.io/fission/fission-bundle:$TAG

# Build fetcher
docker build -t ghcr.io/fission/fetcher:$TAG -f cmd/fetcher/Dockerfile --build-arg TARGETPLATFORM=dist-local .
minikube image load ghcr.io/fission/fetcher:$TAG

# Build pre-upgrade-checks
docker build -t ghcr.io/fission/pre-upgrade-checks:$TAG -f cmd/preupgradechecks/Dockerfile --build-arg TARGETPLATFORM=dist-local .
minikube image load ghcr.io/fission/pre-upgrade-checks:$TAG

# Build reporter
docker build -t ghcr.io/fission/reporter:$TAG -f cmd/reporter/Dockerfile --build-arg TARGETPLATFORM=dist-local .
minikube image load ghcr.io/fission/reporter:$TAG

echo ""
echo "✓ All images built and loaded into Minikube!"
echo "✓ Artifacts are in $DIST_DIR"
echo ""
echo "To install the chart, run:"
echo ""
echo "  helm upgrade --install fission $DIST_DIR/fission-all-2.0.0-local.tgz \\"
echo "    --namespace fission --create-namespace \\"
echo "    --set repository=ghcr.io/fission \\"
echo "    --set imageTag=$TAG \\"
echo "    --set fetcher.repository=ghcr.io/fission \\"
echo "    --set fetcher.imageTag=$TAG \\"
echo "    --set preUpgradeChecks.repository=ghcr.io/fission \\"
echo "    --set preUpgradeChecks.imageTag=$TAG \\"
echo "    --set analytics=false \\"
echo "    --set pullPolicy=Never"
echo ""
