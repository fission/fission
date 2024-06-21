FROM alpine:3.17
RUN apk add --update ca-certificates
COPY fission-bundle /
ENTRYPOINT ["/fission-bundle"]
