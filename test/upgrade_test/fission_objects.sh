#!/bin/bash
set -eu

ns="fission"
ROOT=$(pwd)
PREV_STABLE_VERSION=v1.16.3
HELM_VARS_PREV_RELEASE="routerServiceType=NodePort,analytics=false"
HELM_VARS_LATEST_RELEASE="routerServiceType=NodePort,repository=ghcr.io,image=fission/fission-bundle,pullPolicy=IfNotPresent,imageTag=latest,fetcher.image=fission/fetcher,fetcher.imageTag=latest,postInstallReportImage=fission/reporter,preUpgradeChecks.image=fission/pre-upgrade-checks,preUpgradeChecks.imageTag=latest,analytics=false"

doit() {
    echo "! $*"
    "$@"
}

dump_system_info() {
    echo "System Info"
    doit go version
    doit docker version
    doit kubectl version
    doit helm version
}

install_stable_release() {
    doit helm repo add fission-charts https://fission.github.io/fission-charts/
    doit helm repo update

    echo "Creating namespace $ns"
    doit kubectl create ns $ns

    echo "Creating CRDs"
    doit kubectl create -k "github.com/fission/fission/crds/v1?ref=$PREV_STABLE_VERSION"

    echo "Installing Fission $PREV_STABLE_VERSION"
    doit helm install --debug --wait --version $PREV_STABLE_VERSION \
        --namespace $ns fission fission-charts/fission-all --set $HELM_VARS_PREV_RELEASE

    echo "Download fission cli $PREV_STABLE_VERSION"
    curl -Lo fission https://github.com/fission/fission/releases/download/$PREV_STABLE_VERSION/fission-$PREV_STABLE_VERSION-linux-amd64 && chmod +x fission && sudo mv fission /usr/local/bin/
    doit fission version
}

create_fission_objects() {
    echo "-----------------#########################################--------------------"
    echo "                   Preparing for fission object creation"
    echo "-----------------#########################################--------------------"
    echo "Creating function environment."
    if fission env create --name nodejs --image ghcr.io/fission/node-env; then
        echo "Successfully created function environment"
    else
        echo "Environment creation failed"
        exit 1
    fi

    echo "Creating function"
    curl -LO https://raw.githubusercontent.com/fission/examples/main/nodejs/hello.js
    if fission function create --name hello --env nodejs --code hello.js; then
        echo "Successfully created function"
    else
        echo "Function creation failed"
        exit 1
    fi

    echo "Creating HTTPTrigger so the function can be invoked via the public router URL"
    if fission route create --name hello --url /hello --method GET --function hello; then
        echo "Successfully created HTTPTrigger"
    else
        echo "HTTPTrigger creation failed"
        exit 1
    fi

    sleep 5
}

test_fission_objects() {
    doit fission env list
    doit fission function list
    doit fission httptrigger list
    doit fission check -v 2
    echo "-----------------###############################--------------------"
    echo "                   Running fission object tests"
    echo "-----------------###############################--------------------"
    # Invoke via the HTTPTrigger we created in create_fission_objects rather
    # than via `fission function test`. The CLI's `function test` subcommand
    # used to invoke /fission-function/<name> directly on the public router,
    # which is no longer registered there after the GHSA-3g33-6vg6-27m8
    # listener split. HTTPTriggers are the supported public surface.
    local pf_log="/tmp/upgrade-test-pf.log"
    kubectl port-forward svc/router 18888:80 -n "$ns" >"$pf_log" 2>&1 &
    local pf_pid=$!
    trap "kill $pf_pid 2>/dev/null || true" RETURN

    # Wait up to 30s for the port-forward to accept connections.
    local i
    for i in $(seq 1 30); do
        if curl -sS --connect-timeout 1 --max-time 2 -o /dev/null "http://127.0.0.1:18888/" 2>/dev/null; then
            break
        fi
        if [ "$i" -eq 30 ]; then
            echo "router port-forward did not become ready in 30s; pf log:"
            cat "$pf_log" || true
            kill $pf_pid 2>/dev/null || true
            exit 1
        fi
        sleep 1
    done

    # Poll the HTTPTrigger URL until the function is invokable. Function
    # specialization can take a few seconds on a freshly-upgraded router.
    for i in $(seq 1 30); do
        if curl -fsS --max-time 5 "http://127.0.0.1:18888/hello" >/tmp/upgrade-test-resp 2>&1; then
            echo "----------------------**********************-------------------------"
            echo "                           Test success"
            echo "----------------------**********************-------------------------"
            cat /tmp/upgrade-test-resp || true
            kill $pf_pid 2>/dev/null || true
            return 0
        fi
        sleep 2
    done

    echo "----------------------**********************-------------------------"
    echo "                            Test failed"
    echo "----------------------**********************-------------------------"
    cat /tmp/upgrade-test-resp || true
    kill $pf_pid 2>/dev/null || true
    exit 1
}

build_docker_images() {
    echo "Building new docker images"
    make skaffold-prebuild
    doit docker buildx build -t fission-bundle dist/fission-bundle_linux_amd64_v1 --platform linux/amd64
    doit docker buildx build -t fetcher dist/fetcher_linux_amd64_v1 --platform linux/amd64
    doit docker buildx build -t builder dist/builder_linux_amd64_v1 --platform linux/amd64
    doit docker buildx build -t reporter dist/reporter_linux_amd64_v1 --platform linux/amd64
    doit docker buildx build -t preupgradechecks dist/pre-upgrade-checks_linux_amd64_v1 --platform linux/amd64
}

kind_image_load() {
    # Re-tag locally-built images to match the registry prefix the chart
    # resolves to (HELM_VARS_LATEST_RELEASE: ghcr.io/fission/<name>:latest).
    # Without this, kubelet with pullPolicy=IfNotPresent looks for
    # `ghcr.io/fission/fission-bundle:latest` and falls through to GHCR,
    # pulling whatever was last published from main — which won't have
    # any flags / templates added since that publish.
    echo "Re-tagging local images to match HELM_VARS_LATEST_RELEASE..."
    doit docker tag fission-bundle ghcr.io/fission/fission-bundle:latest
    doit docker tag fetcher ghcr.io/fission/fetcher:latest
    doit docker tag builder ghcr.io/fission/builder:latest
    doit docker tag reporter ghcr.io/fission/reporter:latest
    doit docker tag preupgradechecks ghcr.io/fission/pre-upgrade-checks:latest

    echo "Loading Docker images into Kind cluster."
    doit kind load docker-image ghcr.io/fission/fission-bundle:latest --name kind
    doit kind load docker-image ghcr.io/fission/fetcher:latest --name kind
    doit kind load docker-image ghcr.io/fission/builder:latest --name kind
    doit kind load docker-image ghcr.io/fission/reporter:latest --name kind
    doit kind load docker-image ghcr.io/fission/pre-upgrade-checks:latest --name kind
}

install_fission_cli() {
    echo "Installing new Fission cli"
    doit make build-fission-cli
    doit sudo make install-fission-cli
    sudo chmod +x /usr/local/bin/fission
}

install_current_release() {
    echo "Running Fission upgrade"
    doit helm dependency update "$ROOT"/charts/fission-all
    doit make update-crds
    doit helm upgrade --debug --wait --namespace $ns --set $HELM_VARS_LATEST_RELEASE fission "$ROOT"/charts/fission-all
    doit kubectl rollout status deployment/router -n $ns --watch --timeout 2m
}

"$@"
