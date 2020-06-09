ARG GO_VERSION=1.14

FROM ubuntu:18.04 AS base
WORKDIR /
RUN apt update && apt install -y ca-certificates && rm -rf /var/lib/apt/lists/*

FROM golang:${GO_VERSION} AS builder

ENV GOPATH /usr
ENV APP	   ${GOPATH}/src/github.com/fission/fission/mqtrigger/kafkaDeployment

WORKDIR ${APP}

ADD main.go ${APP}

RUN go get
RUN go build -a -o main .

CMD ["./main"]