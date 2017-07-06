FROM alpine:3.5

RUN apk update

COPY . /app
WORKDIR /app

EXPOSE 8888
ENTRYPOINT ["./server"]