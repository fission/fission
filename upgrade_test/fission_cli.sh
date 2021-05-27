#!/bin/bash
set -x


setup_fission_cli() {
      go build -ldflags "-X github.com/fission/fission/pkg/info.GitCommit=XTYGHJGHJG -X github.com/fission/fission/pkg/info.BuildDate=May-26-2021 -X github.com/fission/fission/pkg/info.Version=1.12" -o fission ./cmd/fission-cli/main.go
      sudo mv fission /usr/local/bin
      fission version
    
}

setup_fission_cli