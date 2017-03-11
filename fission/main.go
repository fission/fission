/*
Copyright 2016 The Fission Authors.

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

package main

import (
	"os"

	"github.com/urfave/cli"
)

func main() {
	app := cli.NewApp()
	app.Name = "fission"
	app.Usage = "Serverless functions for Kubernetes"

	app.Flags = []cli.Flag{
		cli.StringFlag{Name: "server", Usage: "Fission server URL", EnvVar: "FISSION_URL"},
	}

	// trigger method and url flags (used in function and route CLIs)
	htMethodFlag := cli.StringFlag{Name: "method", Usage: "HTTP Method: GET|POST|PUT|DELETE|HEAD; defaults to GET"}
	htUrlFlag := cli.StringFlag{Name: "url", Usage: "URL pattern (See gorilla/mux supported patterns)"}

	// functions
	fnNameFlag := cli.StringFlag{Name: "name", Usage: "function name"}
	fnEnvNameFlag := cli.StringFlag{Name: "env", Usage: "environment name for function"}
	fnCodeFlag := cli.StringFlag{Name: "code", Usage: "local path or URL for source code"}
	fnPackageFlag := cli.StringFlag{Name: "package", Usage: "local path or URL for binary package"}
	fnUidFlag := cli.StringFlag{Name: "uid", Usage: "function uid, optional (use latest if unspecified)"}
	fnPodFlag := cli.StringFlag{Name: "pod", Usage: "function pod name, optional (use latest if unspecified)"}
	fnFollowFlag := cli.BoolFlag{Name: "follow, f", Usage: "specify if the logs should be streamed"}
	fnDetailFlag := cli.BoolFlag{Name: "detail, d", Usage: "display detailed information"}
	fnLogDBHostFlag := cli.StringFlag{Name: "dbhost", Usage: "log database host to connect to", EnvVar: "FISSION_LOGDB"}
	fnLogDBTypeFlag := cli.StringFlag{Name: "dbtype", Usage: "log database type, e.g. influxdb (currently only influxdb is supported)"}
	fnUserNameFlag := cli.StringFlag{Name: "username, u", Usage: "username for connecting log database"}
	fnPasswordFlag := cli.StringFlag{Name: "password, p", Usage: "password for connecting log database"}
	fnSubcommands := []cli.Command{
		{Name: "create", Usage: "Create new function (and optionally, an HTTP route to it)", Flags: []cli.Flag{fnNameFlag, fnEnvNameFlag, fnCodeFlag, fnPackageFlag, htUrlFlag, htMethodFlag}, Action: fnCreate},
		{Name: "get", Usage: "Get function source code", Flags: []cli.Flag{fnNameFlag, fnUidFlag}, Action: fnGet},
		{Name: "edit", Usage: "Edit function source code in $EDITOR", Flags: []cli.Flag{fnNameFlag, fnUidFlag}, Action: fnEdit},
		{Name: "getmeta", Usage: "Get function metadata", Flags: []cli.Flag{fnNameFlag, fnUidFlag}, Action: fnGetMeta},
		{Name: "update", Usage: "Update function source code", Flags: []cli.Flag{fnNameFlag, fnEnvNameFlag, fnCodeFlag, fnPackageFlag}, Action: fnUpdate},
		{Name: "delete", Usage: "Delete function", Flags: []cli.Flag{fnNameFlag, fnUidFlag}, Action: fnDelete},
		{Name: "list", Usage: "List all functions", Flags: []cli.Flag{}, Action: fnList},
		{Name: "logs", Usage: "Display funtion logs", Flags: []cli.Flag{fnNameFlag, fnPodFlag, fnFollowFlag, fnDetailFlag, fnLogDBHostFlag, fnLogDBTypeFlag, fnUserNameFlag, fnPasswordFlag}, Action: fnLogs},
		{Name: "pods", Usage: "Display funtion pods", Flags: []cli.Flag{fnNameFlag, fnLogDBHostFlag, fnLogDBTypeFlag, fnUserNameFlag, fnPasswordFlag}, Action: fnPods},
	}

	// httptriggers
	htNameFlag := cli.StringFlag{Name: "name", Usage: "HTTP Trigger name"}
	htFnNameFlag := cli.StringFlag{Name: "function", Usage: "Function name"}
	htFnUidFlag := cli.StringFlag{Name: "uid", Usage: "Function UID (optional; uses latest if unspecified)"}
	htSubcommands := []cli.Command{
		{Name: "create", Aliases: []string{"add"}, Usage: "Create HTTP trigger", Flags: []cli.Flag{htMethodFlag, htUrlFlag, htFnNameFlag, htFnUidFlag}, Action: htCreate},
		{Name: "get", Usage: "Get HTTP trigger", Flags: []cli.Flag{htMethodFlag, htUrlFlag}, Action: htGet},
		{Name: "update", Usage: "Update HTTP trigger", Flags: []cli.Flag{htNameFlag, htFnNameFlag, htFnUidFlag}, Action: htUpdate},
		{Name: "delete", Usage: "Delete HTTP trigger", Flags: []cli.Flag{htNameFlag}, Action: htDelete},
		{Name: "list", Usage: "List HTTP triggers", Flags: []cli.Flag{}, Action: htList},
	}

	// environments
	envNameFlag := cli.StringFlag{Name: "name", Usage: "Environment name"}
	envImageFlag := cli.StringFlag{Name: "image", Usage: "Environment image URL"}
	envSubcommands := []cli.Command{
		{Name: "create", Aliases: []string{"add"}, Usage: "Add an environment", Flags: []cli.Flag{envNameFlag, envImageFlag}, Action: envCreate},
		{Name: "get", Usage: "Get environment details", Flags: []cli.Flag{envNameFlag}, Action: envGet},
		{Name: "update", Usage: "Update environment", Flags: []cli.Flag{envNameFlag, envImageFlag}, Action: envUpdate},
		{Name: "delete", Usage: "Delete environment", Flags: []cli.Flag{envNameFlag}, Action: envDelete},
		{Name: "list", Usage: "List all environments", Flags: []cli.Flag{}, Action: envList},
	}

	// watches
	wNameFlag := cli.StringFlag{Name: "name", Usage: "Watch name"}
	wFnNameFlag := cli.StringFlag{Name: "function", Usage: "Function name"}
	wFnUidFlag := cli.StringFlag{Name: "uid", Usage: "Function UID (optional; uses latest if unspecified)"}
	wNamespaceFlag := cli.StringFlag{Name: "ns", Usage: "Namespace of resource to watch"}
	wObjTypeFlag := cli.StringFlag{Name: "type", Usage: "Type of resource to watch (Pod, Service, etc.)"}
	wLabelsFlag := cli.StringFlag{Name: "labels", Usage: "Label selector of the form a=b,c=d"}
	wSubCommands := []cli.Command{
		{Name: "create", Aliases: []string{"add"}, Usage: "Create a watch", Flags: []cli.Flag{wFnNameFlag, wFnUidFlag, wNamespaceFlag, wObjTypeFlag, wLabelsFlag}, Action: wCreate},
		{Name: "get", Usage: "Get details about a watch", Flags: []cli.Flag{wNameFlag}, Action: wGet},
		// TODO add update flag when supported
		{Name: "delete", Usage: "Delete watch", Flags: []cli.Flag{wNameFlag}, Action: wDelete},
		{Name: "list", Usage: "List all watches", Flags: []cli.Flag{}, Action: wList},
	}

	app.Commands = []cli.Command{
		{Name: "function", Aliases: []string{"fn"}, Usage: "Create, update and manage functions", Subcommands: fnSubcommands},
		{Name: "httptrigger", Aliases: []string{"ht", "route"}, Usage: "Manage HTTP triggers (routes) for functions", Subcommands: htSubcommands},
		{Name: "environment", Aliases: []string{"env"}, Usage: "Manage environments", Subcommands: envSubcommands},
		{Name: "watch", Aliases: []string{"w"}, Usage: "Manage watches", Subcommands: wSubCommands},

		// Misc commands
		{
			Name:  "get-deployment-yaml",
			Usage: "Get deployment yaml.  Use it as 'fission get-deployment-yaml | kubectl create -f -'",
			Action: func(c *cli.Context) error {
				getDeploymentYaml()
				return nil
			},
		},
	}

	app.Run(os.Args)
}
