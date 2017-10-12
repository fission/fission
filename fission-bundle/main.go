package main

import (
	"log"
	"os"
	"strconv"

	"github.com/fission/fission/executor"

	"github.com/docopt/docopt-go"
	"github.com/fission/fission/buildermgr"
	"github.com/fission/fission/controller"
	"github.com/fission/fission/kubewatcher"
	"github.com/fission/fission/logger"
	"github.com/fission/fission/mqtrigger"
	"github.com/fission/fission/poolmgr"
	"github.com/fission/fission/router"
	"github.com/fission/fission/storagesvc"
	"github.com/fission/fission/timer"
)

func runController(port int) {
	controller.Start(port)
	log.Fatalf("Error: Controller exited.")
}

func runRouter(port int, executorUrl string) {
	router.Start(port, executorUrl)
	log.Fatalf("Error: Router exited.")
}

func runExecutor(port int, poolmgrUrl string) {
	err := executor.StartExecutor(poolmgrUrl, port)
	if err != nil {
		log.Fatalf("Error starting executor: %v", err)
	}
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

func runStorageSvc(port int, filePath string) {
	subdir := os.Getenv("SUBDIR")
	if len(subdir) == 0 {
		subdir = "fission-functions"
	}
	storagesvc.RunStorageService(storagesvc.StorageTypeLocal,
		filePath, subdir, port)
}

func runBuilderMgr(port int, storageSvcUrl string, envBuilderNamespace string) {
	err := buildermgr.Start(port, storageSvcUrl, envBuilderNamespace)
	if err != nil {
		log.Fatalf("Error starting buildermgr: %v", err)
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

 Controller is a stateless API frontend for fission resources.

 Pool manager maintains a pool of generalized function containers, and
 specializes them on-demand. Poolmgr must be run from a pod in a
 Kubernetes cluster.

 Router implements HTTP triggers: it routes to running instances,
 working with the controller and poolmgr.

 Kubewatcher implements Kubernetes Watch triggers: it watches
 Kubernetes resources and invokes functions described in the
 KubernetesWatchTrigger.

 The storage service implements storage for functions too large to fit
 in the Kubernetes API resource object. It supports various storage
 backends.

Usage:
  fission-bundle --controllerPort=<port>
  fission-bundle --routerPort=<port> [--executorUrl=<url>]
  fission-bundle --executorPort=<port> [--poolmgrUrl=<url>]
  fission-bundle --poolmgrPort=<port> [--namespace=<namespace>] [--fission-namespace=<namespace>]
  fission-bundle --kubewatcher [--routerUrl=<url>]
  fission-bundle --storageServicePort=<port> --filePath=<filePath>
  fission-bundle --builderMgrPort=<port> [--storageSvcUrl=<url>] [--envbuilder-namespace=<namespace>]
  fission-bundle --logger
  fission-bundle --timer [--routerUrl=<url>]
  fission-bundle --mqt   [--routerUrl=<url>]
Options:
  --controllerPort=<port>         Port that the controller should listen on.
  --routerPort=<port>             Port that the router should listen on.
  --poolmgrPort=<port>            Port that the poolmgr should listen on.
  --executorPort=<port>           Port that the executor should listen on.
  --storageServicePort=<port>     Port that the storage service should listen on.
  --builderMgrPort=<port>         Port that the buildermgr should listen on.
  --poolmgrUrl=<url>              Poolmgr URL. Not required if --poolmgrPort is specified.
  --executorUrl=<url>             Executor URL. Not required if --executorPort is specified.
  --routerUrl=<url>               Router URL.
  --etcdUrl=<etcdUrl>             Etcd URL.
  --storageSvcUrl=<url>           StorageService URL.
  --filePath=<filePath>           Directory to store functions in.
  --namespace=<namespace>         Kubernetes namespace in which to run function containers. Defaults to 'fission-function'.
  --kubewatcher                   Start Kubernetes events watcher.
  --logger                        Start logger.
  --timer                         Start Timer.
  --mqt                           Start message queue trigger.
`
	arguments, err := docopt.Parse(usage, nil, true, "fission-bundle", false)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	functionNs := getStringArgWithDefault(arguments["--namespace"], "fission-function")
	fissionNs := getStringArgWithDefault(arguments["--fission-namespace"], "fission")
	envBuilderNs := getStringArgWithDefault(arguments["--envbuilder-namespace"], "fission-builder")

	executorUrl := getStringArgWithDefault(arguments["--executorUrl"], "http://executor.fission")
	poolmgrUrl := getStringArgWithDefault(arguments["--poolmgrUrl"], "http://poolmgr.fission")
	routerUrl := getStringArgWithDefault(arguments["--routerUrl"], "http://router.fission")
	storageSvcUrl := getStringArgWithDefault(arguments["--storageSvcUrl"], "http://storagesvc.fission")

	if arguments["--controllerPort"] != nil {
		port := getPort(arguments["--controllerPort"])
		runController(port)
	}

	if arguments["--routerPort"] != nil {
		port := getPort(arguments["--routerPort"])
		runRouter(port, executorUrl)
	}

	if arguments["--executorPort"] != nil {
		port := getPort(arguments["--executorPort"])
		runExecutor(port, poolmgrUrl)
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

	if arguments["--storageServicePort"] != nil {
		port := getPort(arguments["--storageServicePort"])
		filePath := arguments["--filePath"].(string)
		runStorageSvc(port, filePath)
	}

	if arguments["--builderMgrPort"] != nil {
		port := getPort(arguments["--builderMgrPort"])
		runBuilderMgr(port, storageSvcUrl, envBuilderNs)
	}

	select {}
}
