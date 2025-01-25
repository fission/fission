export REPOSITORY=ghcr.io/fission
export NODE_RUNTIME_IMAGE=${REPOSITORY}/node-env-22
export NODE_BUILDER_IMAGE=${REPOSITORY}/node-builder-22
export PYTHON_RUNTIME_IMAGE=${REPOSITORY}/python-env
export PYTHON_BUILDER_IMAGE=${REPOSITORY}/python-builder
export GO_RUNTIME_IMAGE=${REPOSITORY}/go-env-1.23
export GO_BUILDER_IMAGE=${REPOSITORY}/go-builder-1.23
export JVM_RUNTIME_IMAGE=${REPOSITORY}/jvm-env
export JVM_BUILDER_IMAGE=${REPOSITORY}/jvm-builder
export JVM_JERSEY_RUNTIME_IMAGE=${REPOSITORY}/jvm-jersey-env-22
export JVM_JERSEY_BUILDER_IMAGE=${REPOSITORY}/jvm-jersey-builder-22
export TS_RUNTIME_IMAGE=${REPOSITORY}/tensorflow-serving-env

IMAGES=(
    "$NODE_RUNTIME_IMAGE"
    "$NODE_BUILDER_IMAGE"
    "$PYTHON_RUNTIME_IMAGE"
    "$PYTHON_BUILDER_IMAGE"
    "$JVM_RUNTIME_IMAGE"
    "$JVM_BUILDER_IMAGE"
    "$JVM_JERSEY_RUNTIME_IMAGE"
    "$JVM_JERSEY_BUILDER_IMAGE"
    "$GO_RUNTIME_IMAGE"
    "$GO_BUILDER_IMAGE"
    "$TS_RUNTIME_IMAGE"
)

for IMG in "${IMAGES[@]}"; do
    if ! docker manifest inspect "$IMG" >/dev/null; then
        echo "Missing image: $IMG"
    else
        echo "Found image: $IMG"
    fi
done
