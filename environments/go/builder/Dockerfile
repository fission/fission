ARG BUILDER_IMAGE=fission/builder
FROM ${BUILDER_IMAGE}

FROM golang:1.9.2

COPY --from=0 /builder /builder

ENV GOPATH /usr
RUN go get github.com/fission/fission

ADD build.sh /usr/local/bin/build

