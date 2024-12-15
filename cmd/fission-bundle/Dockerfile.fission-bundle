FROM alpine:3.21.0
RUN apk add --update ca-certificates
COPY fission-bundle /
ENTRYPOINT ["/fission-bundle"]
