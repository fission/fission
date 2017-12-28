ARG BUILDER_IMAGE=fission/builder
ARG GO_VERSION=1.9.2

FROM ${BUILDER_IMAGE}

FROM golang:${GO_VERSION}

COPY --from=0 /builder /builder

ENV GOPATH /usr
RUN go get github.com/fission/fission

ADD build.sh /usr/local/bin/build

