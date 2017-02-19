#!/bin/sh
GOOS=linux GOARCH=386 go build
docker build -t fission-logger:latest .
