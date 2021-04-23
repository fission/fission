#!/usr/bin/env bash

# Copyright 2015 The Kubernetes Authors.
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

# Generates `types_swagger_doc_generated.go` files for API group
# versions. That file contains functions on API structs that return
# the comments that should be surfaced for the corresponding API type
# in our API docs.


#
# Please refer https://github.com/kubernetes/kubernetes/tree/master/hack for original file
#

set -o errexit
set -o nounset
set -o pipefail

FISSION_CRD_VERSION=v1

source "swagger.sh"

# To avoid compile errors, remove the currently existing files.

for group_version in "${FISSION_CRD_VERSION}"; do
  kube::swagger::gen_types_swagger_doc "${group_version}" ../../${FISSION_CRD_VERSION}
done
