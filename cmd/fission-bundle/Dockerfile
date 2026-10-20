# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

FROM cgr.dev/chainguard/static:latest@sha256:fe55470f22d3259488d9d3739168d8f04da67755f0b69382bc26eda4a7d3d327
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/fission-bundle /
ENTRYPOINT ["/fission-bundle"]
