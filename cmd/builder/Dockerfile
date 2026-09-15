# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

FROM cgr.dev/chainguard/static:latest@sha256:bf639cba19ba56329e6907ac26a7afcdde57a80b6aa66d5100da6883196e6b82
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/builder /builder
ENTRYPOINT ["/builder"]
