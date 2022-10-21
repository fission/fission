# Copyright 2017 The Fission Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

.DEFAULT_GOAL := check

SKAFFOLD_PROFILE ?= kind

VERSION ?= v0.0.0
TIMESTAMP ?= $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
COMMITSHA ?= $(shell git rev-parse HEAD)

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
GOAMD64 ?= $(shell go env GOAMD64)

FISSION-CLI-SUFFIX :=
ifeq ($(GOOS), windows)
	FISSION-CLI-SUFFIX := .exe
endif

# Show this help.
help:
	@awk '/^#/{c=substr($$0,3);next}c&&/^[[:alpha:]][[:alnum:]_-]+:/{print substr($$1,1,index($$1,":")),c}1{c=0}' $(MAKEFILE_LIST) | column -s: -t

print-%:
	@echo '$*=$($*)'

debug-vars: print-GOOS print-GOARCH print-GOAMD64 print-VERSION print-TIMESTAMP print-COMMITSHA print-FISSION-CLI-SUFFIX print-SKAFFOLD_PROFILE

### Static checks
check: test-run build-fission-cli clean

code-checks:
	golangci-lint run

# run basic check scripts
test-run: code-checks
	hack/runtests.sh
	@rm -f coverage.txt

### Binaries
build-fission-cli:
	@GOOS=$(GOOS) GOARCH=$(GOARCH) GOAMD64=$(GOAMD64) GORELEASER_CURRENT_TAG=$(VERSION) goreleaser build --snapshot --rm-dist --single-target --id fission-cli

install-fission-cli:
	# TODO: Fix this hack, replace v1 with GOAMD64
	mv dist/fission-cli_$(GOOS)_$(GOARCH)_v1/fission$(FISSION-CLI-SUFFIX) /usr/local/bin/fission

### Codegen
codegen: controller-gen-install
	@controller-gen object:headerFile="hack/boilerplate.txt" paths="./..."
	@./hack/update-codegen.sh

### CRDs
controller-gen-install:
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.9.2

generate-crds: controller-gen-install
	controller-gen crd \
	paths=./pkg/apis/core/v1  \
	output:crd:artifacts:config=crds/v1

# TODO: this is not thoroughly tested  
generate-webhooks:  controller-gen-install
	controller-gen crd webhook \
	 paths=./pkg/apis/core/v1 \
	 output:crd:artifacts:config=pkg/admission-webhook/config

create-crds:
	@kubectl create -k crds/v1

update-crds:
	@kubectl replace -k crds/v1

delete-crds:
	@kubectl delete -k crds/v1

### Cleanup
clean:
	@rm -f dist/

### Misc
generate-swagger-doc:
	@./hack/update-swagger-docs.sh

generate-cli-docs:
	go run tools/cmd-docs/main.go -o "../fission.io/content/en/docs/reference/fission-cli"

install-crd-ref-docs:
	go install github.com/elastic/crd-ref-docs@master

generate-crd-ref-docs: install-crd-ref-docs
	# crd-ref-docs: https://github.com/elastic/crd-ref-docs
	crd-ref-docs --source-path=pkg/apis/core/v1 --config=tools/crd-ref-docs/config.yaml --renderer markdown
	cp tools/crd-ref-docs/header.md crd_docs.md
	cat out.md >> crd_docs.md && rm out.md
	mv crd_docs.md ../fission.io/content/en/docs/reference/crd-reference.md

all-generators: codegen generate-crds generate-swagger-doc generate-cli-docs generate-crd-ref-docs

skaffold-prebuild:
	@GOOS=linux GOARCH=amd64 GORELEASER_CURRENT_TAG=$(VERSION) goreleaser build --snapshot --rm-dist --single-target
	@cp -v cmd/builder/Dockerfile.fission-builder dist/builder_linux_amd64_v1/Dockerfile
	@cp -v cmd/fetcher/Dockerfile.fission-fetcher dist/fetcher_linux_amd64_v1/Dockerfile
	@cp -v cmd/fission-bundle/Dockerfile.fission-bundle dist/fission-bundle_linux_amd64_v1/Dockerfile
	@cp -v cmd/reporter/Dockerfile.reporter dist/reporter_linux_amd64_v1/Dockerfile
	@cp -v cmd/preupgradechecks/Dockerfile.fission-preupgradechecks dist/pre-upgrade-checks_linux_amd64_v1/Dockerfile

skaffold-deploy: skaffold-prebuild
	skaffold run -p $(SKAFFOLD_PROFILE)

### Release
release:
	@./hack/generate-helm-manifest.sh $(VERSION)
	@./hack/release.sh $(VERSION)
	@./hack/release-tag.sh $(VERSION)
	@./hack/changelog.sh
