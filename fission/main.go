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
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/skratchdot/open-golang/open"
	"github.com/urfave/cli"
	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/pkg/api"
	"k8s.io/client-go/1.5/pkg/api/v1"
	"k8s.io/client-go/1.5/tools/clientcmd"
)

// source from k8s client-go out-of-cluster-client-configuration example
func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}

func getKubernetesClient() (*kubernetes.Clientset, error) {
	var kubeconfig string
	if home := homeDir(); home != "" {
		kubeconfig = filepath.Join(home, ".kube", "config")
	} else {
		return nil, errors.New("Kube config is not found in home folder")
	}

	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}

	// create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return clientset, nil
}

func isArgExist(strings []string, key string) bool {
	for _, s := range strings {
		if s == key {
			return true
		}
	}
	return false
}

func getNodeIP(client *kubernetes.Clientset) (string, error) {
	nodes, err := client.Nodes().List(api.ListOptions{})
	if err != nil {
		return "", err
	}
	if len(nodes.Items) < 1 {
		return "", errors.New("No available nodes are found.")
	}
	for _, address := range nodes.Items[0].Status.Addresses {
		if address.Type == v1.NodeExternalIP {
			return address.Address, nil
		}
	}
	return "", errors.New("No available external ip found")
}

func getFissionEnvVariable(svcName string) string {
	k8sClientset, err := getKubernetesClient()
	if err != nil {
		fmt.Printf("#Error occured getting k8s client: %v\n", err)
		return ""
	}

	controller, err2 := k8sClientset.Core().Services("fission").Get(svcName)
	if err2 != nil {
		fmt.Printf("#Error occured getting fission: %v\n", svcName)
		return ""
	}

	switch controller.Spec.Type {
	case v1.ServiceTypeNodePort:
		// TODO: find the port via name
		ip, err3 := getNodeIP(k8sClientset)
		if err3 != nil {
			panic(err3)
		}
		return fmt.Sprintf("http://%v:%v",
			ip, controller.Spec.Ports[0].NodePort)
	case v1.ServiceTypeLoadBalancer:
		return fmt.Sprintf("http://%v:%v",
			controller.Status.LoadBalancer.Ingress[0].IP, controller.Spec.Ports[0].Port)
	default:

	}
	return ""
}

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
	fnLogDBTypeFlag := cli.StringFlag{Name: "dbtype", Usage: "log database type, e.g. influxdb (currently only influxdb is supported)"}
	fnSubcommands := []cli.Command{
		{Name: "create", Usage: "Create new function (and optionally, an HTTP route to it)", Flags: []cli.Flag{fnNameFlag, fnEnvNameFlag, fnCodeFlag, fnPackageFlag, htUrlFlag, htMethodFlag}, Action: fnCreate},
		{Name: "get", Usage: "Get function source code", Flags: []cli.Flag{fnNameFlag, fnUidFlag}, Action: fnGet},
		{Name: "edit", Usage: "Edit function source code in $EDITOR", Flags: []cli.Flag{fnNameFlag, fnUidFlag}, Action: fnEdit},
		{Name: "getmeta", Usage: "Get function metadata", Flags: []cli.Flag{fnNameFlag, fnUidFlag}, Action: fnGetMeta},
		{Name: "update", Usage: "Update function source code", Flags: []cli.Flag{fnNameFlag, fnEnvNameFlag, fnCodeFlag, fnPackageFlag}, Action: fnUpdate},
		{Name: "delete", Usage: "Delete function", Flags: []cli.Flag{fnNameFlag, fnUidFlag}, Action: fnDelete},
		{Name: "list", Usage: "List all functions", Flags: []cli.Flag{}, Action: fnList},
		{Name: "logs", Usage: "Display funtion logs", Flags: []cli.Flag{fnNameFlag, fnPodFlag, fnFollowFlag, fnDetailFlag, fnLogDBTypeFlag}, Action: fnLogs},
		{Name: "pods", Usage: "Display funtion pods", Flags: []cli.Flag{fnNameFlag, fnLogDBTypeFlag}, Action: fnPods},
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

	// timetriggers
	ttNameFlag := cli.StringFlag{Name: "name", Usage: "Time Trigger name"}
	ttCronFlag := cli.StringFlag{Name: "cron", Usage: "Time Trigger cron spec ('0 30 * * *', '@every 5m', '@hourly')"}
	ttFnNameFlag := cli.StringFlag{Name: "function", Usage: "Function name"}
	ttFnUidFlag := cli.StringFlag{Name: "uid", Usage: "Function UID (optional; uses latest if unspecified)"}
	ttSubcommands := []cli.Command{
		{Name: "create", Aliases: []string{"add"}, Usage: "Create Time trigger", Flags: []cli.Flag{ttNameFlag, ttFnNameFlag, ttFnUidFlag, ttCronFlag}, Action: ttCreate},
		{Name: "get", Usage: "Get Time trigger", Flags: []cli.Flag{}, Action: ttGet},
		{Name: "update", Usage: "Update Time trigger", Flags: []cli.Flag{ttNameFlag, ttCronFlag}, Action: ttUpdate},
		{Name: "delete", Usage: "Delete Time trigger", Flags: []cli.Flag{ttNameFlag}, Action: ttDelete},
		{Name: "list", Usage: "List Time triggers", Flags: []cli.Flag{}, Action: ttList},
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
		{Name: "timetrigger", Aliases: []string{"tt", "timer"}, Usage: "Manage Time triggers (timers) for functions", Subcommands: ttSubcommands},
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
		{
			Name:  "get-env-variables",
			Usage: "Get FISSION_URL from k8s client.  Use it as 'source <(fission get-env-variables)'",
			Action: func(c *cli.Context) error {
				// find fission server url via k8s client if FISSION_URL or server is not set
				vars := getFissionEnvVariable("controller")
				if vars != "" {
					fmt.Printf("export %v=%v", "FISSION_URL", vars)
					return nil
				}
				return errors.New("Failed to get FISSION_URL")
			},
		},
		{
			Name:  "ui",
			Usage: "Open ui in browser, show installation guide if ui not installed.",
			Action: func(c *cli.Context) error {
				// find fission ui url via k8s client and open it in browser
				ui := getFissionEnvVariable("fission-ui")
				if ui == "" {
					fmt.Println("UI not installed, please follow the guide to install it")
					open.Run("https://github.com/fission/fission/blob/master/INSTALL.md#use-the-web-based-fission-ui-optional")
				} else {
					fmt.Printf("Opening ui: %v\n", ui)
					open.Run(ui)
				}
				return nil
			},
		},
	}

	app.Run(os.Args)
}
