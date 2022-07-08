FROM alpine:3.16
RUN apk add --update ca-certificates
COPY fetcher /
ENTRYPOINT ["/fetcher"]
