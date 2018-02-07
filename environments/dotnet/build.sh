#!/bin/sh

THIS_DIR=$(realpath $(dirname $0))

docker run -v $THIS_DIR:/proj microsoft/dotnet:1.1-sdk sh -c "cd /proj && ./project-build.sh"

