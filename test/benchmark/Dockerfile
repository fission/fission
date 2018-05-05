FROM golang:1.10.1 AS go-builder
WORKDIR /go
RUN go get github.com/wcharczuk/go-chart
COPY picasso.go /go
RUN CGO_ENABLE=0 GOOS=linux GOARCH=amd64 go build -o picasso .

FROM loadimpact/k6
WORKDIR /fission-bench
COPY --from=go-builder /go/picasso /usr/local/bin/picasso
RUN apk --update add --no-cache bash curl
RUN curl -Lo fission https://github.com/fission/fission/releases/download/$(curl --silent "https://api.github.com/repos/fission/fission/releases/latest" | grep "tag_name" |sed -E 's/.*"([^"]+)".*/\1/')/fission-cli-linux && chmod +x fission && mv fission /usr/local/bin/

ENTRYPOINT ["sh"]
