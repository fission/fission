FROM cgr.dev/chainguard/static:latest@sha256:7bdd9720cefba78e8133c4d03eaaf3f18a25d147d2c8803cc830061e46b6b474
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/fission-bundle /
ENTRYPOINT ["/fission-bundle"]
