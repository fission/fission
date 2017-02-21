FROM golang:1.8rc3-wheezy

ADD server.go /go/src/
WORKDIR /go/src
RUN go build -o /server server.go

ENTRYPOINT ["/server"]
EXPOSE 8888
