package main

import (
	"log"
	"os"
	"strconv"
	//"time"

	"github.com/docopt/docopt-go"
	"github.com/platform9/fission/controller"
	"github.com/platform9/fission/poolmgr"
	"github.com/platform9/fission/router"
)

func runController(port int, etcdUrl string, filepath string) {
	_, err := os.Stat(filepath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Fatalf("Error: path %v does not exist", filepath)
		} else {
			log.Fatalf("Error: can't access path %v", filepath)
		}
	}
	fileStore := controller.MakeFileStore(filepath)

	rs, err := controller.MakeResourceStore(fileStore, []string{etcdUrl})
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	api := &controller.API{
		FunctionStore:    controller.FunctionStore{ResourceStore: *rs},
		HTTPTriggerStore: controller.HTTPTriggerStore{ResourceStore: *rs},
		EnvironmentStore: controller.EnvironmentStore{ResourceStore: *rs},
	}
	api.Serve(port)
	log.Fatalf("Error: Controller exited.")
}

func runRouter(port int, controllerUrl string, poolmgrUrl string) {
	router.Start(port, controllerUrl, poolmgrUrl)
	log.Fatalf("Error: Router exited.")
}

func runPoolmgr(port int, controllerUrl string, namespace string) {
	err := poolmgr.StartPoolmgr(controllerUrl, namespace, port)
	if err != nil {
		log.Fatalf("Error starting poolmgr: %v", err)
	}
}

func getPort(portArg interface{}) int {
	portArgStr := portArg.(string)
	port, err := strconv.Atoi(portArgStr)
	if err != nil {
		log.Fatalf("Error: invalid port number '%v'", portArgStr)
	}
	return port
}

func getStringArgWithDefault(arg interface{}, defaultValue string) string {
	if arg != nil {
		return arg.(string)
	} else {
		return defaultValue
	}
}

func main() {
	usage := `fission-bundle: Package of all fission microservices: controller, router, poolmgr.

Use it to start one or more of the fission servers:

 Controller keeps track of functions, triggers, environments.

 Pool manager maintains a pool of generalized function containers, and specializes them on-demand. Poolmgr must be run from a pod in a Kubernetes cluster.

 Router implements HTTP triggers: it routes to running instances, working with the controller and poolmgr.

Usage:
  fission-bundle --controllerPort=<port> [--etcdUrl=<etcdUrl>] --filepath=<filepath>
  fission-bundle --routerPort=<port> [--controllerUrl=<url> --poolmgrUrl=<url>]
  fission-bundle --poolmgrPort=<port> [--controllerUrl=<url>]
Options:
  --controllerPort=<port>  Port that the controller should listen on.
  --routerPort=<port>      Port that the router should listen on.
  --poolmgrPort=<port>     Port that the poolmgr should listen on.
  --controllerUrl=<url>    Controller URL. Not required if --controllerPort is specified.
  --poolmgrUrl=<url>       Controller URL. Not required if --poolmgrPort is specified.
  --etcdUrl=<etcdUrl>      Etcd URL.
  --filepath=<filepath>    Directory to store functions in.
  --namespace=<namespace>  Kubernetes namespace in which to run function containers. Defaults to 'fission-function'.
`
	arguments, err := docopt.Parse(usage, nil, true, "fission-bundle", false)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	namespace := getStringArgWithDefault(arguments["--namespace"], "fission-function")
	controllerUrl := getStringArgWithDefault(arguments["--controllerUrl"], "http://controller")
	etcdUrl := getStringArgWithDefault(arguments["--etcdUrl"], "http://etcd:2379")
	poolmgrUrl := getStringArgWithDefault(arguments["--poolmgrUrl"], "http://poolmgr")

	if arguments["--controllerPort"] != nil {
		port := getPort(arguments["--controllerPort"])
		runController(port, etcdUrl, arguments["--filepath"].(string))
	}

	if arguments["--routerPort"] != nil {
		port := getPort(arguments["--routerPort"])
		runRouter(port, controllerUrl, poolmgrUrl)
	}

	if arguments["--poolmgrPort"] != nil {
		port := getPort(arguments["--poolmgrPort"])
		runPoolmgr(port, controllerUrl, namespace)
	}

	select {}
}
