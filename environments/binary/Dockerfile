FROM golang:1.13-alpine

WORKDIR /go
COPY *.go /go/

RUN go build -o server .

FROM alpine:3.10

WORKDIR /app

RUN apk update
RUN apk add coreutils binutils findutils grep

COPY --from=0 /go/server /app/server

EXPOSE 8888
ENTRYPOINT ["./server"]
