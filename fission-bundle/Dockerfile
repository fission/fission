FROM alpine:3.4
RUN apk add --update ca-certificates
ADD fission-bundle /
COPY config.yaml /etc/fission/config.yaml
