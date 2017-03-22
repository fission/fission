# This file originally came from official Kubernetes GitHub repository.
# You can reach original file with the following link:
# https://github.com/kubernetes/kubernetes/tree/42fbf93fb0bb48d0592e2aa08c5ce6d28ab6d4b0/cluster/addons/fluentd-gcp/fluentd-gcp-image

#!/bin/sh

# Copyright 2016 The Kubernetes Authors.
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

# For systems without journald
mkdir -p /var/log/journal

LD_PRELOAD=/opt/td-agent/embedded/lib/libjemalloc.so
RUBY_GC_HEAP_OLDOBJECT_LIMIT_FACTOR=0.9

/usr/sbin/td-agent $@
