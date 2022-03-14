set -e
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags  '-w -s  -extldflags "-static"' -o ./dist/fission-bundle_linux_amd64/fission-bundle cmd/fission-bundle/main.go
tag=1.15.12
cd ./dist/fission-bundle_linux_amd64
docker buildx build --platform linux/amd64 -t xytschool/fission-bundle:${tag} . -f Dockerfile
docker push xytschool/fission-bundle:${tag}