#!/usr/bin/env bash
#
# Set tools for OS compatibility.
#
# Prerequisite on Mac:
#   brew install coreutils gnu-sed parallel

if [ $(uname -s) == 'Darwin' ]; then
    timeout() {
        gtimeout "$@"
    }
    export -f timeout

    date() {
        gdate "$@"
    }
    export -f date

    sed() {
      gsed "$@"
    }
    export -f sed

    readlink() {
        greadlink "$@"
    }
    export -f readlink

    find_executable() {
        path=$1; shift
        find $path -perm +111 -type f "$@"
    }

else
    find_executable() {
        path=$1; shift
        find $path -executable -type f "$@"
    }
fi
