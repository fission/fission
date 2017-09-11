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

    ./build.sh
    docker build -t $tag .
   
    popd
}

# Push fission-bundle image
push_fission_bundle_image() {
    version=$1
    tag=fission/fission-bundle:$version
    docker push $tag
}

build_fetcher_image() {
    version=$1
    tag=fission/fetcher:$version

    pushd $DIR/environments/fetcher/cmd

    ./build.sh
    docker build -t $tag .

    popd    
}

push_fetcher_image() {
    version=$1
    tag=fission/fetcher:$version
    docker push $tag
}

build_and_push_env_image() {
    version=$1
    envdir=$2
    imgname=$3

    echo "Building $envdir -> $imgname:$version"
    
    pushd $DIR/environments/$envdir
    if [ -f build.sh ]
    then
       ./build.sh
    fi
    docker build -t fission/$imgname:$version .
    docker push fission/$imgname:$version
    popd
}

build_and_push_all_envs() {
    version=$1

    build_and_push_env_image $version nodejs node-env
    build_and_push_env_image $version binary binary-env
    build_and_push_env_image $version dotnet dotnet-env
    build_and_push_env_image $version go go-env
    build_and_push_env_image $version perl perl-env
    build_and_push_env_image $version php7 php-env
    build_and_push_env_image $version python3 python-env
    build_and_push_env_image $version ruby ruby-env  
}

build_charts() {
    version=$1
    mkdir -p $BUILDDIR/charts
    pushd $DIR/charts
    for c in all core
    do
	tgz=fission-$c-$version.tgz
	tar czvf $tgz fission-$c/
	mv $tgz $BUILDDIR/charts/
    done
    popd
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
    build_fetcher_image $version
    build_all_cli
    build_charts $version
}

push_all() {
    push_fission_bundle_image $version
    push_fetcher_image $version    
}

tag_and_release() {
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
	   --pre-release
}

attach_github_release_cli() {
    version=$1
    gittag=$version
    # cli
    echo "Uploading osx cli"
    gothub upload \
	   --replace \
	   --user fission \
	   --repo fission \
	   --tag $gittag \
	   --name fission-cli-osx \
	   --file $BUILDDIR/cli/osx/fission

    echo "Uploading linux cli"
    gothub upload \
	   --replace \
	   --user fission \
	   --repo fission \
	   --tag $gittag \
	   --name fission-cli-linux \
	   --file $BUILDDIR/cli/linux/fission

    echo "Uploading windows cli"
    gothub upload \
	   --replace \
	   --user fission \
	   --repo fission \
	   --tag $gittag \
	   --name fission-cli-windows.exe \
	   --file $BUILDDIR/cli/windows/fission.exe
}

attach_github_release_charts() {
    version=$1
    gittag=$version

    # helm charts
    gothub upload \
	   --replace \
	   --user fission \
	   --repo fission \
	   --tag $gittag \
	   --name fission-all-$version.tgz \
	   --file $BUILDDIR/charts/fission-all-$version.tgz

    gothub upload \
	   --replace \
	   --user fission \
	   --repo fission \
	   --tag $gittag \
	   --name fission-core-$version.tgz \
	   --file $BUILDDIR/charts/fission-core-$version.tgz

}

export GITHUB_TOKEN=$(cat ~/.gh-access-token)

check_branch
check_clean
version=$1

build_all $version
push_all $version
build_and_push_all_envs $version 
build_charts $version

tag_and_release $version
attach_github_release_cli $version
attach_github_release_charts $version
