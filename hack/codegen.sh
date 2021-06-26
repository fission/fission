if [ ! -d "../code-generator" ]; then
	echo "Please get code-generator from fission org"
	exit 1
fi

export GOPATH=$(go env GOPATH)
../code-generator/generate-groups.sh all \
	github.com/fission/fission/pkg/generated \
	github.com/fission/fission/pkg/apis "core:v1" \
	--go-header-file ./pkg/apis/boilerplate.txt
