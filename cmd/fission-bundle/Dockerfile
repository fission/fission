# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

FROM cgr.dev/chainguard/static:latest@sha256:24dd7ff8788fdfadda39eeeaefefb6d1cec6002a545935a5f7e017484053734f
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/fission-bundle /
ENTRYPOINT ["/fission-bundle"]
