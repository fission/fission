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