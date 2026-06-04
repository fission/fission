#!/bin/bash
set -e

echo "Waiting for GitHub Pages to update (allowing 30s)..."
current_ver=$(helm search repo fission-custom -l | grep "2.0.0-watch-all-namespaces" | wc -l)

# Simple loop to wait for the version to appear
for i in {1..10}; do
    helm repo update
    if helm search repo fission-custom -l | grep -q "2.0.0-watch-all-namespaces"; then
        echo "Found version 2.0.0-watch-all-namespaces!"
        break
    fi
    echo "Version not found yet. Retrying in 10s..."
    sleep 10
done

echo "Cleaning up old installation..."
helm uninstall fission -n fission || true
kubectl delete ns fission || true

echo "Installing Fission 2.0.0-watch-all-namespaces..."
helm install fission fission-custom/fission-all \
  --namespace fission \
  --create-namespace \
  --version 2.0.0-watch-all-namespaces

echo "Done! Verifying pods..."
kubectl get pods -n fission
