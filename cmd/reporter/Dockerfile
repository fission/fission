FROM cgr.dev/chainguard/static:latest@sha256:9cef3c6a78264cb7e25923bf1bf7f39476dccbcc993af9f4ffeb191b77a7951e
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/reporter /
ENTRYPOINT ["/reporter"]
