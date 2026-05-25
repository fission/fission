# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

doit() {
    echo "! $*"
    "$@"
}

version=$1
if [ -z $version ]; then
    echo "Release version not mentioned"
    exit 1
fi

# tag the release
doit git tag $version
# push tag
doit git push origin $version