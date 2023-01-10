FROM alpine:3.16
RUN apk add --update ca-certificates
COPY pre-upgrade-checks /
ENTRYPOINT ["/pre-upgrade-checks"]
