#!/bin/sh

set -e
#set -x

DIR=$(realpath $(dirname $0))/../
BUILDDIR=$(realpath $DIR)/build

# Ensure we're on the master branch
check_branch() {
    curr_branch=$(git rev-parse --abbrev-ref HEAD)
    if $curr_branch != "master"
    then
	echo "Not on master branch."
	exit 1
    fi
}

# Ensure working dir is clean
check_clean() {    
    if ! git diff-index --quiet HEAD --
    then
	echo "Unclean tree"
	exit 1
    fi
}

# Build CLI binaries for mac/linux/windows
build_all_cli() {
    build_cli "linux" "linux"
    build_cli "darwin" "osx"
    build_cli "windows" "windows"
}

# Build cli binary for one OS, and put it in $BUILDDIR/cli/<os>/
build_cli() {
    os=$1
    osName=$2
    arch="amd64" # parameterize if/when we need to
    
    pushd $DIR/fission
    GOOS=$os GOARCH=$arch go build .

    if [ "$os" == "windows" ]
    then
	binary=fission.exe
    else
	binary=fission
    fi

    outdir=$BUILDDIR/cli/$osName/
    mkdir -p $outdir
    mv $binary $outdir
    
    popd
}

# Build fission-bundle image
build_fission_bundle_image() {
    version=$1
    tag=fission/fission-bundle:$version

    pushd $DIR/fission-bundle

    GOOS=linux go build 
    docker build -t $tag .
    
    popd
}

# Push fission-bundle image
push_fission_bundle_image() {
    version=$1
    tag=fission/fission-bundle:$version
    docker push $tag
}

build_all() {
    version=$1
    if [ -z "$version" ]
    then
	echo "Version unspecified"
	exit 1
    fi
    
    if [ -e $BUILDDIR ]
    then
	echo "Removing existing build dir ($BUILDDIR)."
	rm -rf $BUILDDIR
    fi
    
    mkdir -p $BUILDDIR
    
    build_fission_bundle_image $version
    build_all_cli
}

make_github_release() {
    version=$1
    gittag=$version

    # tag the release
    git tag $gittag
    
    # push tag
    git push --tags

    # create gh release
    gothub release \
	   --user fission \
	   --repo fission \
	   --tag $gittag \
	   --name "$version" \
	   --description "$version" \

    # attach files

    # cli
    gothub upload \
	   --user fission \
	   --repo fission \
	   --tag $gittag \
	   --name fission-cli-osx \
	   --file $BUILDDIR/cli/osx/fission

    gothub upload \
	   --user fission \
	   --repo fission \
	   --tag $gittag \
	   --name fission-cli-linux \
	   --file $BUILDDIR/cli/linux/fission

    gothub upload \
	   --user fission \
	   --repo fission \
	   --tag $gittag \
	   --name fission-cli-windows.exe \
	   --file $BUILDDIR/cli/windows/fission.exe    
}


check_branch
check_clean
version=$1

build_all $version
push_fission_bundle_image $version
make_github_release $version
