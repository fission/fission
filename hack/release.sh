#!/bin/sh

set -e
#set -x

DIR=$(realpath $(dirname $0))/../
BUILDDIR=$(realpath $DIR)/build

# Ensure we're on the master branch
check_branch() {
    version=$1
    curr_branch=$(git rev-parse --abbrev-ref HEAD)
    if [ $curr_branch != "v${version}" ]
    then
	echo "Not on v${version} branch."
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

    if [ "$osName" == "windows" ]
    then
	binary=fission-cli-${osName}.exe
    else
	binary=fission-cli-${osName}
    fi

    GOOS=$os GOARCH=$arch go build -gcflags=-trimpath=$GOPATH -asmflags=-trimpath=$GOPATH -o $binary .

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
    docker tag $tag fission/fission-bundle:latest
   
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
    docker tag $tag fission/fetcher:latest

    popd    
}

push_fetcher_image() {
    version=$1
    tag=fission/fetcher:$version
    docker push $tag
}

build_builder_image() {
    version=$1
    tag=fission/builder:$version

    pushd $DIR/builder/cmd

    ./build.sh
    docker build -t $tag .

    popd
}

push_builder_image() {
    version=$1
    tag=fission/builder:$version
    docker push $tag
}

build_logger_image() {
    version=$1
    tag=fission/fluentd:$version

    pushd $DIR/logger/fluentd

    docker build -t $tag .

    popd
}

push_logger_image() {
    version=$1
    tag=fission/fluentd:$version
    docker push $tag
}

build_and_push_logger_image() {
    build_logger_image $1
    push_logger_image $1
}

build_and_push_env_image() {
    version=$1
    envdir=$2
    imgnamebase=$3
    imgvariant=$4

    if [ -z "$imgvariant" ]
    then 
        # no variant specified, just use the base name
        imgname=$imgnamebase
        dockerfile="Dockerfile"
    else 
        # variant specified - append variant to image name and assume dockerfile 
        # exists with same suffix (e.g. image node-env-debian built from Dockerfile-debian)
        imgname="$imgname-$imgvariant"
        dockerfile="Dockerfile-$imgvariant"
    fi
    echo "Building $envdir -> $imgname:$version using $dockerfile"
    
    pushd $DIR/environments/$envdir
    if [ -f build.sh ]
    then
       ./build.sh
    fi  
    docker build -t fission/$imgname:$version -f $dockerfile .
    docker tag fission/$imgname:$version fission/$imgname:latest
    docker push fission/$imgname:$version
    docker push fission/$imgname:latest
    popd
}

build_and_push_all_envs() {
    version=$1

    # call with version, env dir, image name base, image name variant
    build_and_push_env_image "$version" "nodejs"   "node-env"     ""
    build_and_push_env_image "$version" "nodejs"   "node-env"     "debian"
    build_and_push_env_image "$version" "binary"   "binary-env"   ""
    build_and_push_env_image "$version" "dotnet"   "dotnet-env"   ""
    build_and_push_env_image "$version" "dotnet20" "dotnet20-env" ""    
    build_and_push_env_image "$version" "go"       "go-env"       ""
    build_and_push_env_image "$version" "perl"     "perl-env"     ""
    build_and_push_env_image "$version" "php7"     "php-env"      ""
    build_and_push_env_image "$version" "python"   "python-env"   ""
    build_and_push_env_image "$version" "python"   "python-env"   "2.7"
    build_and_push_env_image "$version" "ruby"     "ruby-env"     ""
}

build_and_push_env_builder_image() {
    version=$1
    envdir=$2
    imgnamebase=$3
    imgvariant=$4

    if [ -z "$imgvariant" ]
    then
        # no variant specified, just use the base name
        imgname=$imgnamebase
        dockerfile="Dockerfile"
    else
        # variant specified - append variant to image name and assume dockerfile
        # exists with same suffix (e.g. image node-env-debian built from Dockerfile-debian)
        imgname="$imgname-$imgvariant"
        dockerfile="Dockerfile-$imgvariant"
    fi
    echo "Building $envdir -> $imgname:$version using $dockerfile"

    pushd $DIR/environments/$envdir/builder
    docker build -t fission/$imgname:$version -f $dockerfile .
    docker tag fission/$imgname:$version fission/$imgname:latest
    docker push fission/$imgname:$version
    docker push fission/$imgname:latest
    popd
}

build_and_push_all_env_builders() {
    version=$1

    # call with version, env dir, image name base, image name variant
    build_and_push_env_builder_image "$version" "python"   "python-builder"   ""
    build_and_push_env_builder_image "$version" "binary"   "binary-builder"   ""
    build_and_push_env_builder_image "$version" "go"       "go-builder"       ""
}

build_charts() {
    version=$1
    mkdir -p $BUILDDIR/charts
    pushd $DIR/charts
    find . -iname *.~?~ | xargs rm
    for c in fission-all fission-core
    do
	helm package $c/
	mv *.tgz $BUILDDIR/charts/
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
    build_builder_image $version
    build_logger_image $version
    build_all_cli
    build_charts $version
}

push_all() {
    push_fission_bundle_image $version
    push_fission_bundle_image latest
    push_fetcher_image $version
    push_fetcher_image latest
    push_builder_image $version
    push_logger_image $version
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
	   --file $BUILDDIR/cli/osx/fission-cli-osx

    echo "Uploading linux cli"
    gothub upload \
	   --replace \
	   --user fission \
	   --repo fission \
	   --tag $gittag \
	   --name fission-cli-linux \
	   --file $BUILDDIR/cli/linux/fission-cli-linux

    echo "Uploading windows cli"
    gothub upload \
	   --replace \
	   --user fission \
	   --repo fission \
	   --tag $gittag \
	   --name fission-cli-windows.exe \
	   --file $BUILDDIR/cli/windows/fission-cli-windows.exe
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

generate_changelog() {
    version=$1

    echo "# ${version}" > new_CHANGELOG.md
    echo
    echo "[Documentation](http://fission.io/docs/${version}/)" >> new_CHANGELOG.md
    echo

    create_downloads_table ${version} >> new_CHANGELOG.md

    # generate changelog from github
    github_changelog_generator fission/fission -t ${GITHUB_TOKEN} --future-release ${version} --no-issues -o tmp_CHANGELOG.md
    sed -i '' -e '$ d' tmp_CHANGELOG.md

    # concatenate two files
    cat tmp_CHANGELOG.md >> new_CHANGELOG.md
    mv new_CHANGELOG.md ${DIR}/CHANGELOG.md

    rm tmp_CHANGELOG.md
}

create_downloads_table () {
  release_tag=$1
  url_prefix="https://github.com/fission/fission/releases/download"

  echo "## Downloads for ${version}"
  echo

  files=$(find build -name '*' -type f)

  echo
  echo "filename | sha256 hash"
  echo "-------- | -----------"
  for file in $files; do
    echo "[${file##*/}]($url_prefix/$release_tag/${file##*/}) | \`$(shasum -a 256 $file | cut -d' ' -f 1)\`"
  done
  echo
}
export -f create_downloads_table

export GITHUB_TOKEN=$(cat ~/.gh-access-token)


version=$1
check_branch $version
check_clean

build_all $version
push_all $version
build_and_push_all_envs $version
build_and_push_all_env_builders $version
build_charts $version

tag_and_release $version
attach_github_release_cli $version
attach_github_release_charts $version

generate_changelog $version
