# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

FROM cgr.dev/chainguard/static:latest@sha256:96d02f455d5a73b817c0602910748609cf8471b1cc9522f78c75cedb1f67d072
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/reporter /
ENTRYPOINT ["/reporter"]
