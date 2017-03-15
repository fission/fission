#!/bin/sh

if [ $# -ne 1 ]; then
    echo "usage: ./build hello|hellostr" 1>&2
    exit 1
fi

FISSION_PATH="github.com/fission/fission"

docker run --rm -v $GOPATH/src/$FISSION_PATH:/usr/src/$FISSION_PATH \
       -e GOPATH=/usr/ \
       -w /usr/src/$FISSION_PATH/examples/go \
       golang:1.8 go build -buildmode=plugin -o $1.so $1.go
