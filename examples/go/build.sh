#!/bin/sh

FISSION_PATH="github.com/fission/fission"

docker run --rm -v $GOPATH/src/$FISSION_PATH:/usr/src/$FISSION_PATH \
       -e GOPATH=/usr/ \
       -w /usr/src/$FISSION_PATH/examples/go \
       golang:1.8 go build -buildmode=plugin -o hello.so hello.go
