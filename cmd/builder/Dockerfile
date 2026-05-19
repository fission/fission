FROM cgr.dev/chainguard/static:latest@sha256:77d8b8925dc27970ec2f48243f44c7a260d52c49cd778288e4ee97566e0cb75b
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/builder /builder
ENTRYPOINT ["/builder"]
