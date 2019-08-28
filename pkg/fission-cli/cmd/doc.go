/*
Copyright 2019 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import "strings"

const (
	GLOBAL_VERBOSITY = "verbosity"
	GLOBAL_PLUGIN    = "plugin"

	FISSION_SERVER = "server"

	RESOURCE_NAME = "name"

	ENVIRONMENT_NAMESPACE          = "envNamespace"
	ENVIRONMENT_NAMESPACE_ALIAS    = "envns"
	ENVIRONMENT_POOLSIZE           = "poolsize"
	ENVIRONMENT_IMAGE              = "image"
	ENVIRONMENT_BUILDER            = "builder"
	ENVIRONMENT_BUILDCOMMAND       = "buildcmd"
	ENVIRONMENT_KEEPARCHIVE        = "keeparchive"
	ENVIRONMENT_EXTERNAL_NETWORK   = "externalnetwork"
	ENVIRONMENT_GRACE_PERIOD       = "graceperiod"
	ENVIRONMENT_GRACE_PERIOD_ALIAS = "period"
	ENVIRONMENT_VERSION            = "version"

	SPEC_SPEC    = "spec"
	SPEC_SPECDIR = "specdir"

	RUNTIME_MINCPU    = "mincpu"
	RUNTIME_MAXCPU    = "maxcpu"
	RUNTIME_MINMEMORY = "minmemory"
	RUNTIME_MAXMEMORY = "maxmemory"
	RUNTIME_MINSCALE  = "minscale"
	RUNTIME_MAXSCALE  = "maxscale"
	RUNTIME_TARGETCPU = "targetcpu"
)

// GetCliFlagName concatenates flag and its alias into a command flag name.
func GetCliFlagName(flags ...string) string {
	return strings.Join(flags, ", ")
}
