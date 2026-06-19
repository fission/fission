# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

FROM golang:1.26 AS go-builder
WORKDIR /src
# test/benchmark is its own Go module (keeps go-chart + its GPL-licensed
# freetype transitive dep out of the root module's dependency graph).
COPY go.mod go.sum ./
RUN go mod download
COPY picasso.go ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /picasso .

FROM loadimpact/k6
WORKDIR /fission-bench
COPY --from=go-builder /picasso /usr/local/bin/picasso
RUN apk --update add --no-cache bash curl
RUN curl -Lo fission https://github.com/fission/fission/releases/download/$(curl --silent "https://api.github.com/repos/fission/fission/releases/latest" | grep "tag_name" |sed -E 's/.*"([^"]+)".*/\1/')/fission-cli-linux && chmod +x fission && mv fission /usr/local/bin/

ENTRYPOINT ["sh"]
