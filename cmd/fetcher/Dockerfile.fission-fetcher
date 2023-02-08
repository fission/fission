FROM alpine:3.17
RUN apk add --update ca-certificates
COPY fetcher /
ENTRYPOINT ["/fetcher"]
