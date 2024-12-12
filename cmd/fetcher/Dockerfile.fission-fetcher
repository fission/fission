FROM alpine:3.21.0
RUN apk add --update ca-certificates
COPY fetcher /
ENTRYPOINT ["/fetcher"]
