package main

import (
	"log"
	"strconv"

	"github.com/docopt/docopt-go"
	"github.com/fission/fission/controller"
	"github.com/fission/fission/kubewatcher"
	"github.com/fission/fission/logger"
	"github.com/fission/fission/mqtrigger"
	"github.com/fission/fission/poolmgr"
	"github.com/fission/fission/router"
	"github.com/fission/fission/timer"
)

func runController(port int) {
	controller.Start(port)
	log.Fatalf("Error: Controller exited.")
}

func runRouter(port int, poolmgrUrl string) {
	router.Start(port, poolmgrUrl)
	log.Fatalf("Error: Router exited.")
}

func runPoolmgr(port int, fissionNamespace, functionNamespace string) {
	err := poolmgr.StartPoolmgr(fissionNamespace, functionNamespace, port)
	if err != nil {
		log.Fatalf("Error starting poolmgr: %v", err)
	}
}

func runKubeWatcher(routerUrl string) {
	err := kubewatcher.Start(routerUrl)
	if err != nil {
		log.Fatalf("Error starting kubewatcher: %v", err)
	}
}

func runLogger() {
	logger.Start()
	log.Fatalf("Error: Logger exited.")
}

func runTimer(routerUrl string) {
	err := timer.Start(routerUrl)
	if err != nil {
		log.Fatalf("Error starting timer: %v", err)
	}
}

func runMessageQueueMgr(routerUrl string) {
	err := messagequeue.Start(routerUrl)
	if err != nil {
		log.Fatalf("Error starting timer: %v", err)
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
  fission-bundle --controllerPort=<port>
  fission-bundle --routerPort=<port> [--poolmgrUrl=<url>]
  fission-bundle --poolmgrPort=<port> [--namespace=<namespace>] [--fission-namespace=<namespace>]
  fission-bundle --kubewatcher [--routerUrl=<url>]
  fission-bundle --logger
  fission-bundle --timer [--routerUrl=<url>]
  fission-bundle --mqt   [--routerUrl=<url>]
Options:
  --controllerPort=<port>  Port that the controller should listen on.
  --routerPort=<port>      Port that the router should listen on.
  --poolmgrPort=<port>     Port that the poolmgr should listen on.
  --poolmgrUrl=<url>       Poolmgr URL. Not required if --poolmgrPort is specified.
  --routerUrl=<url>        Router URL.
  --etcdUrl=<etcdUrl>      Etcd URL.
  --filepath=<filepath>    Directory to store functions in.
  --namespace=<namespace>  Kubernetes namespace in which to run function containers. Defaults to 'fission-function'.
  --kubewatcher            Start Kubernetes events watcher.
  --logger                 Start logger.
  --timer 		           Start Timer.
  --mqt 		           Start message queue trigger.
`
	arguments, err := docopt.Parse(usage, nil, true, "fission-bundle", false)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	functionNs := getStringArgWithDefault(arguments["--namespace"], "fission-function")
	fissionNs := getStringArgWithDefault(arguments["--fission-namespace"], "fission")

	poolmgrUrl := getStringArgWithDefault(arguments["--poolmgrUrl"], "http://poolmgr.fission")
	routerUrl := getStringArgWithDefault(arguments["--routerUrl"], "http://router.fission")

	if arguments["--controllerPort"] != nil {
		port := getPort(arguments["--controllerPort"])
		runController(port)
	}

	if arguments["--routerPort"] != nil {
		port := getPort(arguments["--routerPort"])
		runRouter(port, poolmgrUrl)
	}

	if arguments["--poolmgrPort"] != nil {
		port := getPort(arguments["--poolmgrPort"])
		runPoolmgr(port, fissionNs, functionNs)
	}

	if arguments["--kubewatcher"] == true {
		runKubeWatcher(routerUrl)
	}

	if arguments["--logger"] == true {
		runLogger()
	}

	if arguments["--timer"] == true {
		runTimer(routerUrl)
	}

	if arguments["--mqt"] == true {
		runMessageQueueMgr(routerUrl)
	}

	select {}
}
