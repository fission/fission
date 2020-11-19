ARG GO_VERSION=1.13

FROM tensorflow/serving as serving
RUN apt update && apt install -y ca-certificates && rm -rf /var/lib/apt/lists/*

FROM golang:${GO_VERSION} AS builder

ENV GOPATH /usr
ENV APP	   ${GOPATH}/src/github.com/fission/fission/environments/tensorflow-serving

WORKDIR ${APP}

ADD server.go ${APP}

RUN go get
RUN go build -a -o /server server.go

FROM serving
WORKDIR /
COPY --from=builder /server /

ENTRYPOINT ["/server"]
EXPOSE 8888
