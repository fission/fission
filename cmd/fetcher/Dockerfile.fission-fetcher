FROM alpine:3
RUN apk add --update ca-certificates
COPY fetcher /
ENTRYPOINT ["/fetcher"]
