#!/bin/bash
set -x


getVersion() {
    echo $(git rev-parse HEAD)
}

getDate() {
    echo $(date -u +'%Y-%m-%dT%H:%M:%SZ')
}

getGitCommit() {
    echo $(git rev-parse HEAD)
}


setup_fission_cli() {
      go build -ldflags "-X github.com/fission/fission/pkg/info.GitCommit=$(getGitCommit) -X github.com/fission/fission/pkg/info.BuildDate=$(getDate) -X github.com/fission/fission/pkg/info.Version=$(getVersion)" -o fission ./cmd/fission-cli/main.go
      sudo mv fission /usr/local/bin
      fission version
    
}

setup_fission_cli