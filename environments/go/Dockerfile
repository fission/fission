ARG GO_VERSION=1.9.2

FROM golang:${GO_VERSION} AS builder

ENV GOPATH /usr
ENV APP	   ${GOPATH}/src/github.com/fission/fission/environments/go

ADD context	    ${APP}/context
ADD server.go   ${APP}

WORKDIR ${APP}
RUN go get
RUN CGO_ENABLED=0 GOOS=linux go build -a -ldflags '-extldflags "-static"' -o /server server.go

FROM alpine:3.7
WORKDIR /
COPY --from=builder /server /
RUN apk -U add ca-certificates

ENTRYPOINT ["/server"]
EXPOSE 8888
