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
	"os"
	"strconv"

	docopt "github.com/docopt/docopt-go"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	cnwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/fission/fission/cmd/fission-bundle/mqtrigger"
	"github.com/fission/fission/pkg/buildermgr"
	"github.com/fission/fission/pkg/canaryconfigmgr"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor"
	eclient "github.com/fission/fission/pkg/executor/client"
	"github.com/fission/fission/pkg/info"
	"github.com/fission/fission/pkg/kubewatcher"
	functionLogger "github.com/fission/fission/pkg/logger"
	mqt "github.com/fission/fission/pkg/mqtrigger"
	"github.com/fission/fission/pkg/router"
	"github.com/fission/fission/pkg/storagesvc"
	"github.com/fission/fission/pkg/timer"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"github.com/fission/fission/pkg/utils/manager"
	"github.com/fission/fission/pkg/utils/otel"
	"github.com/fission/fission/pkg/utils/profile"
	"github.com/fission/fission/pkg/webhook"
)

// runWebhook starts admission webhook server
func runWebhook(ctx context.Context, logger *zap.Logger, port int) error {
	return webhook.Start(ctx, logger, cnwebhook.Options{
		Port: port,
	})
}

func runCanaryConfigServer(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger *zap.Logger, mgr manager.Interface) error {
	return canaryconfigmgr.StartCanaryServer(ctx, clientGen, logger, mgr, false)
}

func runRouter(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger *zap.Logger, mgr manager.Interface, port int, executorUrl string) error {
	return router.Start(ctx, clientGen, logger, mgr, port, eclient.MakeClient(logger, executorUrl))
}

func runExecutor(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger *zap.Logger, mgr manager.Interface, port int) error {
	return executor.StartExecutor(ctx, clientGen, logger, mgr, port)
}

func runKubeWatcher(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger *zap.Logger, mgr manager.Interface, routerUrl string) error {
	return kubewatcher.Start(ctx, clientGen, logger, mgr, routerUrl)
}

func runTimer(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger *zap.Logger, mgr manager.Interface, routerUrl string) error {
	return timer.Start(ctx, clientGen, logger, mgr, routerUrl)
}

func runMessageQueueMgr(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger *zap.Logger, mgr manager.Interface, routerUrl string) error {
	return mqtrigger.Start(ctx, clientGen, logger, mgr, routerUrl)
}

// KEDA based MessageQueue Trigger Manager
func runMQManager(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger *zap.Logger, mgr manager.Interface, routerURL string) error {
	return mqt.StartScalerManager(ctx, clientGen, logger, mgr, routerURL)
}

func runStorageSvc(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger *zap.Logger, mgr manager.Interface, port int, storage storagesvc.Storage) error {
	return storagesvc.Start(ctx, clientGen, logger, storage, mgr, port)
}

func runBuilderMgr(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger *zap.Logger, mgr manager.Interface, storageSvcUrl string) error {
	return buildermgr.Start(ctx, clientGen, logger, mgr, storageSvcUrl)
}

func runLogger(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger *zap.Logger) error {
	return functionLogger.Start(ctx, clientGen, logger)
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

	if arguments["--routerPort"] != nil {
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

func exitWithSync(logger *zap.Logger) {
	// Ignore error, safe to ignore as per https://github.com/uber-go/zap/issues/328
	_ = logger.Sync()
	os.Exit(1)
}

func main() {
	mgr := manager.New()
	defer mgr.Wait()

	var err error

	// From https://github.com/containous/traefik/pull/1817/files
	// Tell glog to log into STDERR. Otherwise, we risk
	// certain kinds of API errors getting logged into a directory not
	// available in a `FROM scratch` Docker container, causing glog to abort
	// hard with an exit code > 0.
	// TODO: fix the lint error. Error checking here is causing all components to crash with error "logtostderr not found"
	flag.Set("logtostderr", "true") //nolint: errcheck

	usage := `fission-bundle: Package of all fission microservices: router, executor.

Use it to start one or more of the fission servers:


 Pool manager maintains a pool of generalized function containers, and
 specializes them on-demand. Executor must be run from a pod in a
 Kubernetes cluster.

 Router implements HTTP triggers: it routes to running instances,
 working with the executor.

 Kubewatcher implements Kubernetes Watch triggers: it watches
 Kubernetes resources and invokes functions described in the
 KubernetesWatchTrigger.

 The storage service implements storage for functions too large to fit
 in the Kubernetes API resource object. It supports various storage
 backends.

Usage:
  fission-bundle --canaryConfig
  fission-bundle --routerPort=<port> [--executorUrl=<url>]
  fission-bundle --executorPort=<port> [--namespace=<namespace>] [--fission-namespace=<namespace>]
  fission-bundle --kubewatcher [--routerUrl=<url>]
  fission-bundle --storageServicePort=<port> --storageType=<storateType>
  fission-bundle --builderMgr [--storageSvcUrl=<url>] [--envbuilder-namespace=<namespace>]
  fission-bundle --timer [--routerUrl=<url>]
  fission-bundle --mqt   [--routerUrl=<url>]
  fission-bundle --mqt_keda [--routerUrl=<url>]
  fission-bundle --webhookPort=<port>
  fission-bundle --logger
  fission-bundle --version
Options:
  --canaryConfig		  		  Start canary config server.
  --webhookPort=<port>             Port that the webhook should listen on.
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
	defer exitWithSync(logger)

	ctx := signals.SetupSignalHandler()
	profile.ProfileIfEnabled(ctx, logger, mgr)

	version := fmt.Sprintf("Fission Bundle Version: %s", info.BuildInfo().String())
	arguments, err := docopt.ParseArgs(usage, nil, version)
	if err != nil {
		logger.Error("failed to parse arguments", zap.Error(err))
		return
	}

	shutdown, err := otel.InitProvider(ctx, logger, getServiceName(arguments))
	if err != nil {
		logger.Error("error initializing provider for OTLP", zap.Error(err), zap.Any("argument", arguments))
		return
	}
	if shutdown != nil {
		defer shutdown(ctx)
	}

	executorUrl := getStringArgWithDefault(arguments["--executorUrl"], "http://executor.fission")
	routerUrl := getStringArgWithDefault(arguments["--routerUrl"], "http://router.fission")
	storageSvcUrl := getStringArgWithDefault(arguments["--storageSvcUrl"], "http://storagesvc.fission")
	clientGen := crd.NewClientGenerator()

	if arguments["--webhookPort"] != nil {
		port := getPort(logger, arguments["--webhookPort"])
		err = runWebhook(ctx, logger, port)
		logger.Error("webhook server exited:", zap.Error(err))
		return
	}

	if arguments["--canaryConfig"] == true {
		err := runCanaryConfigServer(ctx, clientGen, logger, mgr)
		if err != nil {
			logger.Error("canary config server exited with error: ", zap.Error(err))
			return
		}
	}

	if arguments["--routerPort"] != nil {
		port := getPort(logger, arguments["--routerPort"])
		err = runRouter(ctx, clientGen, logger, mgr, port, executorUrl)
		if err != nil {
			logger.Error("router exited", zap.Error(err))
			return
		}
	}

	if arguments["--executorPort"] != nil {
		port := getPort(logger, arguments["--executorPort"])
		err = runExecutor(ctx, clientGen, logger, mgr, port)
		if err != nil {
			logger.Error("executor exited", zap.Error(err))
			return
		}
	}

	if arguments["--kubewatcher"] == true {
		err = runKubeWatcher(ctx, clientGen, logger, mgr, routerUrl)
		if err != nil {
			logger.Error("kubewatcher exited", zap.Error(err))
			return
		}
	}

	if arguments["--timer"] == true {
		err = runTimer(ctx, clientGen, logger, mgr, routerUrl)
		if err != nil {
			logger.Error("timer exited", zap.Error(err))
			return
		}
	}

	if arguments["--mqt"] == true {
		err = runMessageQueueMgr(ctx, clientGen, logger, mgr, routerUrl)
		if err != nil {
			logger.Error("message queue manager exited", zap.Error(err))
			return
		}
	}

	if arguments["--mqt_keda"] == true {
		err = runMQManager(ctx, clientGen, logger, mgr, routerUrl)
		if err != nil {
			logger.Error("mqt scaler manager exited", zap.Error(err))
			return
		}
	}

	if arguments["--builderMgr"] == true {
		err = runBuilderMgr(ctx, clientGen, logger, mgr, storageSvcUrl)
		if err != nil {
			logger.Error("builder manager exited", zap.Error(err))
			return
		}
	}

	if arguments["--logger"] == true {
		err = runLogger(ctx, clientGen, logger)
		if err != nil {
			logger.Error("logger exited", zap.Error(err))
		}
		return
	}

	if arguments["--storageServicePort"] != nil {
		port := getPort(logger, arguments["--storageServicePort"])

		var storage storagesvc.Storage

		if arguments["--storageType"] != nil && arguments["--storageType"] == string(storagesvc.StorageTypeS3) {
			storage = storagesvc.NewS3Storage()
		} else if arguments["--storageType"] == string(storagesvc.StorageTypeLocal) {
			storage = storagesvc.NewLocalStorage("/fission")
		}
		err := runStorageSvc(ctx, clientGen, logger, mgr, port, storage)
		if err != nil {
			logger.Error("storage service exited", zap.Error(err))
			return
		}
	}

	<-ctx.Done()
	logger.Error("exiting")
}
