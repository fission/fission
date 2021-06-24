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

# Platforms to build in multi-architecture images.
PLATFORMS ?= linux/amd64,linux/arm64,linux/arm/v7

# Repository prefix and tag to push multi-architecture images to.
REPO ?= fission
TAG ?= dev
DOCKER_FLAGS ?= --push --progress plain

VERSION ?= master
TIMESTAMP ?= $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
COMMITSHA ?= $(shell git rev-parse HEAD)

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

BINDIR ?= build/bin
FISSION-CLI-SUFFIX :=
ifeq ($(GOOS), windows)
	FISSION-CLI-SUFFIX := .exe
endif

GO ?= go
GO_LDFLAGS := -X github.com/fission/fission/pkg/info.GitCommit=$(COMMITSHA) $(GO_LDFLAGS)
GO_LDFLAGS := -X github.com/fission/fission/pkg/info.BuildDate=$(TIMESTAMP)  $(GO_LDFLAGS)
GO_LDFLAGS := -X github.com/fission/fission/pkg/info.Version=$(VERSION) $(GO_LDFLAGS)
GCFLAGS ?= all=-trimpath=$(CURDIR)
ASMFLAGS ?= all=-trimpath=$(CURDIR)

### Static checks
check: test-run build-fission-cli clean

code-checks:
	hack/verify-gofmt.sh
	hack/verify-govet.sh
	hack/verify-staticcheck.sh

# run basic check scripts
test-run: code-checks
	hack/runtests.sh
	@rm -f coverage.txt

### Binaries
fission-cli:
	@mkdir -p $(BINDIR)
	GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build \
	-gcflags '$(GCFLAGS)' \
	-asmflags '$(ASMFLAGS)' \
	-ldflags "$(GO_LDFLAGS)" \
	-o $(BINDIR)/fission-$(VERSION)-$(GOOS)-$(GOARCH)$(FISSION-CLI-SUFFIX) ./cmd/fission-cli

all-fission-cli:
	$(MAKE) fission-cli GOOS=windows GOARCH=amd64
	$(MAKE) fission-cli GOOS=linux GOARCH=amd64
	$(MAKE) fission-cli GOOS=linux GOARCH=arm
	$(MAKE) fission-cli GOOS=linux GOARCH=arm64
	$(MAKE) fission-cli GOOS=darwin GOARCH=amd64

install-fission-cli: fission-cli
	mv $(BINDIR)/fission-$(VERSION)-$(GOOS)-$(GOARCH)$(FISSION-CLI-SUFFIX) /usr/local/bin/

### Container images
FISSION_IMGS := fission-bundle-multiarch-img \
	fetcher-multiarch-img \
	builder-multiarch-img\
	pre-upgrade-checks-multiarch-img \
	reporter-multiarch-img

verify-builder:
	@./hack/buildx.sh $(PLATFORMS)

local-images:
	PLATFORMS=linux/amd64 $(MAKE) all-images

all-images: verify-builder $(FISSION_IMGS)

fission-bundle-multiarch-img: cmd/fission-bundle/Dockerfile.fission-bundle
fetcher-multiarch-img: cmd/fetcher/Dockerfile.fission-fetcher
builder-multiarch-img: cmd/builder/Dockerfile.fission-builder
pre-upgrade-checks-multiarch-img: cmd/preupgradechecks/Dockerfile.fission-preupgradechecks
reporter-multiarch-img: cmd/reporter/Dockerfile.reporter

%-multiarch-img:
	@echo === Building image $(REPO)/$(subst -multiarch-img,,$@):$(TAG) using context $(CURDIR) and dockerfile $<
	docker buildx build --platform=$(PLATFORMS) -t $(REPO)/$(subst -multiarch-img,,$@):$(TAG) \
		--build-arg GITCOMMIT=$(COMMITSHA) \
    	--build-arg BUILDDATE=$(TIMESTAMP) \
		--build-arg BUILDVERSION=$(VERSION) \
	 	$(DOCKER_FLAGS) -f $< .

### Codegen
codegen:
	@./hack/codegen.sh

### CRDs
generate-crds:
	controller-gen crd:trivialVersions=false,preserveUnknownFields=false  \
	paths=./pkg/apis/core/v1  \
	output:crd:artifacts:config=crds/v1

create-crds:
	@kubectl create -k crds/v1

update-crds:
	@kubectl replace -k crds/v1

delete-crds:
	@kubectl delete -k crds/v1

### Cleanup
clean:
	@rm -f cmd/fission-bundle/fission-bundle
	@rm -f cmd/fission-cli/fission
	@rm -f cmd/fetcher/fetcher
	@rm -f cmd/fetcher/builder
	@rm -f cmd/reporter/reporter
	@rm -f pkg/apis/core/v1/types_swagger_doc_generated.go

### Misc
generate-swagger-doc:
	@cd pkg/apis/core/v1/tool && ./update-generated-swagger-docs.sh

all-generators: codegen generate-crds generate-swagger-doc

release:
	@./hack/release.sh $(VERSION)
	@./hack/release-tag.sh $(VERSION)