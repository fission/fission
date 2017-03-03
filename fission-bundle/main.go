package main

import (
	"log"
	"strconv"

	"github.com/docopt/docopt-go"
	"github.com/fission/fission/controller"
	"github.com/fission/fission/kubewatcher"
	"github.com/fission/fission/logger"
	"github.com/fission/fission/poolmgr"
	"github.com/fission/fission/router"
)

func runController(port int, etcdUrl string, filepath string) {
	// filePath will be created if it doesn't exist.
	fileStore := controller.MakeFileStore(filepath)
	if fileStore == nil {
		log.Fatalf("Failed to initialize filestore")
	}

	rs, err := controller.MakeResourceStore(fileStore, []string{etcdUrl})
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	api := controller.MakeAPI(rs)
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

func runKubeWatcher(controllerUrl, routerUrl string) {
	err := kubewatcher.Start(controllerUrl, routerUrl)
	if err != nil {
		log.Fatalf("Error starting kubewatcher: %v", err)
	}
}

func runLogger() {
	logger.Start()
	log.Fatalf("Error: Logger exited.")
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
  fission-bundle --poolmgrPort=<port> [--controllerUrl=<url> --namespace=<namespace>]
  fission-bundle --kubewatcher [--controllerUrl=<url> --routerUrl=<url>]
  fission-bundle --logger
Options:
  --controllerPort=<port>  Port that the controller should listen on.
  --routerPort=<port>      Port that the router should listen on.
  --poolmgrPort=<port>     Port that the poolmgr should listen on.
  --controllerUrl=<url>    Controller URL. Not required if --controllerPort is specified.
  --poolmgrUrl=<url>       Poolmgr URL. Not required if --poolmgrPort is specified.
  --routerUrl=<url>        Router URL.
  --etcdUrl=<etcdUrl>      Etcd URL.
  --filepath=<filepath>    Directory to store functions in.
  --namespace=<namespace>  Kubernetes namespace in which to run function containers. Defaults to 'fission-function'.
  --kubewatcher            Start Kubernetes events watcher.
  --logger                 Start logger.
`
	arguments, err := docopt.Parse(usage, nil, true, "fission-bundle", false)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	namespace := getStringArgWithDefault(arguments["--namespace"], "fission-function")

	controllerUrl := getStringArgWithDefault(arguments["--controllerUrl"], "http://controller.fission")
	etcdUrl := getStringArgWithDefault(arguments["--etcdUrl"], "http://etcd:2379")
	poolmgrUrl := getStringArgWithDefault(arguments["--poolmgrUrl"], "http://poolmgr.fission")
	routerUrl := getStringArgWithDefault(arguments["--routerUrl"], "http://router.fission")

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

	if arguments["--kubewatcher"] == true {
		runKubeWatcher(controllerUrl, routerUrl)
	}

	if arguments["--logger"] == true {
		runLogger()
	}

	select {}
}
