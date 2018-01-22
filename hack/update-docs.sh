#!/bin/sh

set -x
set -euo pipefail

DIR=$(realpath $(dirname $0))/../
BUILDDIR=$(realpath $DIR)/build
DOCS_SITE=$DIR/Documentation/docs-site

check_hugo() {
    if ! which hugo
    then
	echo "Can't find hugo.  Go here to learn how to install it: https://gohugo.io/getting-started/installing/"
	exit 1
    fi
}

generate_hugo_docs() {
    pushd $DOCS_SITE

    hugo
    
    popd
}

check_website_root() {
    website_root=$1
    pushd $website_root
    # xxx ensure we're in the fission.io repo
    popd
}

copy_hugo_docs() {
    version=$1
    website_root=$2
    
    check_website_root $website_root
    pushd $website_root
    
    mkdir -p docs/$version
    cp -r $DOCS_SITE/public/* docs/$version
    git add docs/$version

    pushd docs
    rm latest
    ln -s $version latest
    git add latest
    popd
    
    popd
}

main() {
    version=$1
    website_root=$2
    
    check_hugo
    generate_hugo_docs
    copy_hugo_docs $version $website_root 
}

version=$1
website_root=$2
main $version $website_root

