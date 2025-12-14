FROM cgr.dev/chainguard/static:latest@sha256:d44809cee093b550944c1f666ff13301f92484bfdd2e53ecaac82b5b6f89647d
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/fetcher /
ENTRYPOINT ["/fetcher"]
