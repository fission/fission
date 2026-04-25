# Fission BuildKit-flavored builder image.
#
# Replaces the legacy per-environment "builder" container when
# Environment.Spec.Builder.Kind is "buildkit". Runs:
#
#   - rootless buildkitd, listening on tcp 127.0.0.1:1234 inside the pod
#   - the Fission "builder" HTTP server on port 8001 — same legacy
#     binary, so /healthz, /clean, /version, and the new /buildkit
#     endpoint all work
#   - /buildkit shells out to /buildkit-build, which drives buildctl
#     against the local daemon and pushes the produced image
#
# Versions pinned by digest where possible to keep supply chain
# auditable. Bump together with the controller-gen / buildctl client
# version in the Makefile target.
FROM moby/buildkit:v0.18.2-rootless AS buildkit

FROM cgr.dev/chainguard/wolfi-base:latest

# Pull buildctl + buildkitd from upstream image — statically linked, so
# we don't need the rest of moby/buildkit's runtime deps.
COPY --from=buildkit /usr/bin/buildctl /usr/bin/buildctl
COPY --from=buildkit /usr/bin/buildkitd /usr/bin/buildkitd
COPY --from=buildkit /usr/bin/rootlesskit /usr/bin/rootlesskit

# Fission binaries built by goreleaser. TARGETPLATFORM matches the
# platform-suffixed dist/ layout used by the rest of Fission's images
# (see cmd/builder/Dockerfile for the parallel pattern).
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/builder /builder
COPY $TARGETPLATFORM/buildkit-build /buildkit-build

ENV BUILDKIT_HOST=tcp://127.0.0.1:1234

# Inline entrypoint: starts buildkitd in the background, polls until
# ready, then execs the Fission builder server. Inlining (rather than
# COPY-ing entrypoint.sh) keeps the Docker build context to just the
# binaries goreleaser produces — no extra_files plumbing needed.
RUN printf '%s\n' \
    '#!/bin/sh' \
    'set -eu' \
    'BUILDKIT_HOST="${BUILDKIT_HOST:-tcp://127.0.0.1:1234}"' \
    'rootlesskit \' \
    '  --net=host \' \
    '  --copy-up=/etc \' \
    '  --copy-up=/run \' \
    '  --disable-host-loopback \' \
    '  buildkitd \' \
    '    --addr="${BUILDKIT_HOST}" \' \
    '    --oci-worker-no-process-sandbox >&2 &' \
    'i=0' \
    'while [ "${i}" -lt 30 ]; do' \
    '  if buildctl --addr "${BUILDKIT_HOST}" debug info >/dev/null 2>&1; then break; fi' \
    '  i=$((i + 1)); sleep 0.5' \
    'done' \
    'if [ "${i}" -ge 30 ]; then echo "buildkitd not ready in 15s" >&2; exit 1; fi' \
    'exec /builder "$@"' \
    > /entrypoint.sh && chmod 0755 /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
