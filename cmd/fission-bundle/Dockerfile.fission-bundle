FROM alpine:3.16
RUN apk add --update ca-certificates
COPY fission-bundle /
ENTRYPOINT ["/fission-bundle"]
