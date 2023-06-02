FROM alpine:3.18
RUN apk add --update ca-certificates
COPY fetcher /
ENTRYPOINT ["/fetcher"]
