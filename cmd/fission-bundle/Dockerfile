FROM alpine:3.18
RUN apk add --update ca-certificates
COPY fission-bundle /
ENTRYPOINT ["/fission-bundle"]
