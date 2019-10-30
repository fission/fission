#!/usr/bin/env bash
#
# Set tools for OS compatibility.
#
# Prerequisite on Mac:
#   brew install coreutils findutils gnu-sed parallel

if [ $(uname -s) == 'Darwin' ]; then
    if command -v gtimeout >/dev/null; then
        timeout() { gtimeout "$@"; }
        export -f timeout
    else
        echo '"gtimeout" command not found. Try "brew install coreutils".'
        exit 1
    fi

    if command -v gdate >/dev/null; then
        date() { gdate "$@"; }
        export -f date
    else
        echo '"gdate" command not found. Try "brew install coreutils".'
        exit 1
    fi

    if command -v gsed >/dev/null; then
        sed() { gsed "$@"; }
        export -f sed
    else
        echo '"gsed" command not found. Try "brew install gnu-sed".'
        exit 1
    fi

    if command -v greadlink >/dev/null; then
        readlink() { greadlink "$@"; }
        export -f readlink
    else
        echo '"greadlink" command not found. Try "brew install coreutils".'
        exit 1
    fi

    if command -v gtr >/dev/null; then
        tr() { gtr "$@"; }
        export -f tr
    else
        echo '"gtr" command not found. Try "brew install coreutils".'
        exit 1
    fi

    if command -v gxargs >/dev/null; then
        xargs() { gxargs "$@"; }
        export -f xargs
    else
        echo '"gxargs" command not found. Try "brew install findutils".'
        exit 1
    fi

    if command -v gsha256sum >/dev/null; then
        sha256sum() { gsha256sum "$@"; }
        export -f sha256sum
    else
        echo '"gsha256sum" command not found. Try "brew install coreutils".'
        exit 1
    fi

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
