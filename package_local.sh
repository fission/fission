#!/bin/bash
set -e

VERSION="v1.22.0-local"
DIST_DIR="./dist-local"

echo "Building Fission Bundle Binary..."
mkdir -p $DIST_DIR
# Build binary
GO_BIN=$HOME/.go_local/go/bin/go
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $GO_BIN build -o $DIST_DIR/fission-bundle ./cmd/fission-bundle

echo "Preparing Helm Chart..."
# Remove old chart copy if exists to prevent nesting
rm -rf $DIST_DIR/fission-all
# Create a copy of the chart
cp -r charts/fission-all $DIST_DIR/fission-all
# Update values to enable the new feature by default
sed -i "s/watchAllNamespaces: false/watchAllNamespaces: true/" $DIST_DIR/fission-all/values.yaml

echo "Packaging Helm Chart..."
helm package $DIST_DIR/fission-all -d $DIST_DIR --version 1.22.0-local --app-version 1.22.0-local

echo "Done!"
TIMESTAMP=$(date +%s)
TAG="local-$TIMESTAMP"

echo "Building Docker Image with tag $TAG..."
docker build -t ghcr.io/fission/fission-bundle:$TAG -f cmd/fission-bundle/Dockerfile --build-arg TARGETPLATFORM=dist-local .
minikube image load ghcr.io/fission/fission-bundle:$TAG

echo "Artifacts are in $DIST_DIR"
echo "To install the chart run:"
echo "  helm upgrade --install fission $DIST_DIR/fission-all-1.22.0-local.tgz --namespace fission --create-namespace --set imageTag=$TAG --set preUpgradeChecks.imageTag=v1.22.0 --set analytics=false --set pullPolicy=Never"
