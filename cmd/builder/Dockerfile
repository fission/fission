FROM cgr.dev/chainguard/static:latest@sha256:a301031ffd4ed67f35ca7fa6cf3dad9937b5fa47d7493955a18d9b4ca5412d1a
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/builder /builder
ENTRYPOINT ["/builder"]
