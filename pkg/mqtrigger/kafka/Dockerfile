FROM golang:1.12-alpine as builder

RUN apk add bash ca-certificates git gcc g++ libc-dev

ARG GOPKG=github.com/fission/fission

ENV GO111MODULE=on

WORKDIR /go/src/${GOPKG}
COPY ./ ./

WORKDIR	 /go/src/${GOPKG}/pkg/mqtrigger/kafka
RUN go build -a -o /go/bin/main

FROM alpine:3.12 as base
RUN apk add --update ca-certificates
COPY --from=builder /go/bin/main /

ENTRYPOINT ["/main"]