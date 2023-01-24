#!/usr/bin/env bash

# This is a sample file for reference and would require further modification.

APIMACHINERY_PKGS=(
    -k8s.io/apimachinery/pkg/util/intstr
    -k8s.io/apimachinery/pkg/api/resource
    -k8s.io/apimachinery/pkg/runtime/schema
    -k8s.io/apimachinery/pkg/runtime
    -k8s.io/apimachinery/pkg/apis/meta/v1
    -k8s.io/api/core/v1
    -k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1
)

# This command auto generates .proto file for types
go-to-protobuf \
    --apimachinery-packages="$(IFS=, ; echo "${APIMACHINERY_PKGS[*]}")" \
    --go-header-file=./hack/boilerplate.txt \
    --only-idl \
    --packages=./pkg/apis/core/v1

# This command generates the client and server stub
protoc \
    -I ./vendor \
    -I ./ \
    ./pkg/executor/proto/executor.proto \
    --go_out=. \
    --go_opt=paths=source_relative \
    --go-grpc_out=. \
    --go-grpc_opt=paths=source_relative