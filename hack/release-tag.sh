doit() {
    echo "! $*"
    "$@"
}

version=$1
if [ -z $version ]; then
    echo "Release version not mentioned"
    exit 1
fi

gittag=$version
prefix="v"
gopkgtag=${version/#/${prefix}}

if [[ ${version} == v* ]]; then # if version starts with "v", don't append prefix.
    gopkgtag=${version}
fi

# tag the release
doit git tag $gittag
doit git tag -a $gopkgtag -m "Fission $gopkgtag"

# push tag
doit git push origin $gittag
doit git push origin $gopkgtag
