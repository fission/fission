#!/bin/sh

THIS_DIR=$(realpath $(dirname $0))

docker run -v $THIS_DIR:/proj microsoft/dotnet:2.0.0-sdk sh -c "cd /proj && ./project-build.sh"

