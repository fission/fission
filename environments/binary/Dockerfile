FROM alpine:3.5

RUN apk update
RUN apk add coreutils binutils findutils grep

COPY . /app
WORKDIR /app

EXPOSE 8888
ENTRYPOINT ["./server"]