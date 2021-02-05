docker run --rm --privileged linuxkit/binfmt:v0.8
docker buildx create --use --name=qemu
docker buildx inspect --bootstrap