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

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"

	docopt "github.com/docopt/docopt-go"

	"go.uber.org/zap"

	"github.com/fission/fission/cmd/fission-bundle/mqtrigger"
	"github.com/fission/fission/pkg/buildermgr"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/executor"
	"github.com/fission/fission/pkg/info"
	"github.com/fission/fission/pkg/kubewatcher"
	functionLogger "github.com/fission/fission/pkg/logger"
	mqt "github.com/fission/fission/pkg/mqtrigger"
	"github.com/fission/fission/pkg/router"
	"github.com/fission/fission/pkg/storagesvc"
	"github.com/fission/fission/pkg/timer"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"github.com/fission/fission/pkg/utils/otel"
	"github.com/fission/fission/pkg/utils/profile"
	"github.com/fission/fission/pkg/utils/tracing"
)

func runController(logger *zap.Logger, port int, openTracingEnabled bool) {
	controller.Start(logger, port, false, openTracingEnabled)
	logger.Fatal("controller exited")
}

func runRouter(logger *zap.Logger, port int, executorUrl string, openTracingEnabled bool) {
	router.Start(logger, port, executorUrl, openTracingEnabled)
	logger.Fatal("router exited")
}

func runExecutor(logger *zap.Logger, port int, functionNamespace, envBuilderNamespace string, openTracingEnabled bool) {
	err := executor.StartExecutor(logger, functionNamespace, envBuilderNamespace, port, openTracingEnabled)
	if err != nil {
		logger.Fatal("error starting executor", zap.Error(err))
	}
}

func runKubeWatcher(logger *zap.Logger, routerUrl string) {
	err := kubewatcher.Start(logger, routerUrl)
	if err != nil {
		logger.Fatal("error starting kubewatcher", zap.Error(err))
	}
}

func runTimer(logger *zap.Logger, routerUrl string) {
	err := timer.Start(logger, routerUrl)
	if err != nil {
		logger.Fatal("error starting timer", zap.Error(err))
	}
}

func runMessageQueueMgr(logger *zap.Logger, routerUrl string) {
	err := mqtrigger.Start(logger, routerUrl)
	if err != nil {
		logger.Fatal("error starting message queue manager", zap.Error(err))
	}
}

// KEDA based MessageQueue Trigger Manager
func runMQManager(logger *zap.Logger, routerURL string) {
	err := mqt.StartScalerManager(logger, routerURL)
	if err != nil {
		logger.Fatal("error starting mqt scaler manager", zap.Error(err))
	}
}

func runStorageSvc(logger *zap.Logger, port int, storage storagesvc.Storage, openTracingEnabled bool) {
	err := storagesvc.Start(logger, storage, port, openTracingEnabled)
	if err != nil {
		logger.Fatal("error starting storage service", zap.Error(err))
	}
}

func runBuilderMgr(logger *zap.Logger, storageSvcUrl string, envBuilderNamespace string) {
	err := buildermgr.Start(logger, storageSvcUrl, envBuilderNamespace)
	if err != nil {
		logger.Fatal("error starting builder manager", zap.Error(err))
	}
}

func runLogger() {
	functionLogger.Start()
	log.Fatalf("Error: Logger exited.")
}

func getPort(logger *zap.Logger, portArg interface{}) int {
	portArgStr := portArg.(string)
	port, err := strconv.Atoi(portArgStr)
	if err != nil {
		logger.Fatal("invalid port number", zap.Error(err), zap.String("port", portArgStr))
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

func getServiceName(arguments map[string]interface{}) string {
	serviceName := "Fission-Unknown"

	if arguments["--controllerPort"] != nil {
		serviceName = "Fission-Controller"
	} else if arguments["--routerPort"] != nil {
		serviceName = "Fission-Router"
	} else if arguments["--executorPort"] != nil {
		serviceName = "Fission-Executor"
	} else if arguments["--kubewatcher"] == true {
		serviceName = "Fission-KubeWatcher"
	} else if arguments["--timer"] == true {
		serviceName = "Fission-Timer"
	} else if arguments["--mqt"] == true {
		serviceName = "Fission-MessageQueueTrigger"
	} else if arguments["--builderMgr"] == true {
		serviceName = "Fission-BuilderMgr"
	} else if arguments["--storageServicePort"] != nil {
		serviceName = "Fission-StorageSvc"
	} else if arguments["--mqt_keda"] == true {
		serviceName = "Fission-Keda-MQTrigger"
	}

	return serviceName
}

func main() {
	var err error

	// From https://github.com/containous/traefik/pull/1817/files
	// Tell glog to log into STDERR. Otherwise, we risk
	// certain kinds of API errors getting logged into a directory not
	// available in a `FROM scratch` Docker container, causing glog to abort
	// hard with an exit code > 0.
	// TODO: fix the lint error. Error checking here is causing all components to crash with error "logtostderr not found"
	flag.Set("logtostderr", "true") //nolint: errcheck

	usage := `fission-bundle: Package of all fission microservices: controller, router, executor.

Use it to start one or more of the fission servers:

 Controller is a stateless API frontend for fission resources.

 Pool manager maintains a pool of generalized function containers, and
 specializes them on-demand. Executor must be run from a pod in a
 Kubernetes cluster.

 Router implements HTTP triggers: it routes to running instances,
 working with the controller and executor.

 Kubewatcher implements Kubernetes Watch triggers: it watches
 Kubernetes resources and invokes functions described in the
 KubernetesWatchTrigger.

 The storage service implements storage for functions too large to fit
 in the Kubernetes API resource object. It supports various storage
 backends.

Usage:
  fission-bundle --controllerPort=<port>
  fission-bundle --routerPort=<port> [--executorUrl=<url>]
  fission-bundle --executorPort=<port> [--namespace=<namespace>] [--fission-namespace=<namespace>]
  fission-bundle --kubewatcher [--routerUrl=<url>]
  fission-bundle --storageServicePort=<port> --storageType=<storateType>
  fission-bundle --builderMgr [--storageSvcUrl=<url>] [--envbuilder-namespace=<namespace>]
  fission-bundle --timer [--routerUrl=<url>]
  fission-bundle --mqt   [--routerUrl=<url>]
  fission-bundle --mqt_keda [--routerUrl=<url>]
  fission-bundle --logger
  fission-bundle --version
Options:
  --controllerPort=<port>         Port that the controller should listen on.
  --routerPort=<port>             Port that the router should listen on.
  --executorPort=<port>           Port that the executor should listen on.
  --storageServicePort=<port>     Port that the storage service should listen on.
  --executorUrl=<url>             Executor URL. Not required if --executorPort is specified.
  --routerUrl=<url>               Router URL.
  --etcdUrl=<etcdUrl>             Etcd URL.
  --storageSvcUrl=<url>           StorageService URL.
  --filePath=<filePath>           Directory to store functions in.
  --namespace=<namespace>         Kubernetes namespace in which to run function containers. Defaults to 'fission-function'.
  --kubewatcher                   Start Kubernetes events watcher.
  --timer                         Start Timer.
  --mqt                           Start message queue trigger.
  --mqt_keda					  Start message queue trigger of kind KEDA
  --builderMgr                    Start builder manager.
  --version                       Print version information
`
	logger := loggerfactory.GetLogger()
	defer logger.Sync()

	profile.ProfileIfEnabled(logger)

	version := fmt.Sprintf("Fission Bundle Version: %v", info.BuildInfo().String())
	arguments, err := docopt.ParseArgs(usage, nil, version)
	if err != nil {
		logger.Fatal("Could not parse command line arguments", zap.Error(err))
	}

	ctx := context.Background()
	openTracingEnabled := tracing.TracingEnabled(logger)
	if openTracingEnabled {
		err = tracing.RegisterTraceExporter(logger, os.Getenv("TRACE_JAEGER_COLLECTOR_ENDPOINT"), getServiceName(arguments))
		if err != nil {
			logger.Fatal("Could not register trace exporter", zap.Error(err), zap.Any("argument", arguments))
		}
	} else {
		shutdown, err := otel.InitProvider(ctx, logger, getServiceName(arguments))
		if err != nil {
			logger.Fatal("error initializing provider for OTLP", zap.Error(err), zap.Any("argument", arguments))
		}
		if shutdown != nil {
			defer shutdown(ctx)
		}
	}

	functionNs := getStringArgWithDefault(arguments["--namespace"], "fission-function")
	envBuilderNs := getStringArgWithDefault(arguments["--envbuilder-namespace"], "fission-builder")

	executorUrl := getStringArgWithDefault(arguments["--executorUrl"], "http://executor.fission")
	routerUrl := getStringArgWithDefault(arguments["--routerUrl"], "http://router.fission")
	storageSvcUrl := getStringArgWithDefault(arguments["--storageSvcUrl"], "http://storagesvc.fission")

	if arguments["--controllerPort"] != nil {
		port := getPort(logger, arguments["--controllerPort"])
		runController(logger, port, openTracingEnabled)
	}

	if arguments["--routerPort"] != nil {
		port := getPort(logger, arguments["--routerPort"])
		runRouter(logger, port, executorUrl, openTracingEnabled)
	}

	if arguments["--executorPort"] != nil {
		port := getPort(logger, arguments["--executorPort"])
		runExecutor(logger, port, functionNs, envBuilderNs, openTracingEnabled)
	}

	if arguments["--kubewatcher"] == true {
		runKubeWatcher(logger, routerUrl)
	}

	if arguments["--timer"] == true {
		runTimer(logger, routerUrl)
	}

	if arguments["--mqt"] == true {
		runMessageQueueMgr(logger, routerUrl)
	}

	if arguments["--mqt_keda"] == true {
		runMQManager(logger, routerUrl)
	}

	if arguments["--builderMgr"] == true {
		runBuilderMgr(logger, storageSvcUrl, envBuilderNs)
	}

	if arguments["--logger"] == true {
		runLogger()
	}

	if arguments["--storageServicePort"] != nil {
		port := getPort(logger, arguments["--storageServicePort"])

		var storage storagesvc.Storage

		if arguments["--storageType"] != nil && arguments["--storageType"] == string(storagesvc.StorageTypeS3) {
			storage = storagesvc.NewS3Storage()
		} else if arguments["--storageType"] == string(storagesvc.StorageTypeLocal) {
			storage = storagesvc.NewLocalStorage("/fission")
		}
		runStorageSvc(logger, port, storage, openTracingEnabled)
	}

	select {}
}
