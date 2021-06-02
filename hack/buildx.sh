set -e
## This script needs to be run to start the emulator for multiarch builds
create_docker_builder() {
    if ! $(docker buildx inspect --bootstrap | grep "^Driver" | grep -q "docker-container"); then
        echo "Starting docker builder fission"
        docker buildx create --name=fission --use --driver=docker-container
        docker buildx inspect --bootstrap
    fi
}

check_platform() {
    if ! $(docker buildx inspect --bootstrap | grep "^Platforms:" | grep -q "$1"); then
        echo "Platform $1 not supported by builder"
        exit 1
    fi
}

create_docker_builder
PLATFORMS=(
    linux/amd64
    linux/arm6455
    linux/arm/v7
)
for platform in "${PLATFORMS[@]}"; do
    check_platform $platform
done
