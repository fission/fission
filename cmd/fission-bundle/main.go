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

// Command line arguments
type CommandLineArgs struct {
	// Flags
	canaryConfig bool
	kubewatcher  bool
	timer        bool
	mqt          bool
	mqt_keda     bool
	builderMgr   bool
	showVersion  bool
	logger       bool

	// Port values
	webhookPort        int
	routerPort         int
	executorPort       int
	storageServicePort int

	// URL values
	executorUrl   string
	routerUrl     string
	storageSvcUrl string

	// Other configurations
	storageType string
}

// Usage information
const usageText string = `fission-bundle: Package of all fission microservices: router, executor.

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
  --storageSvcUrl=<url>           StorageService URL.
  --kubewatcher                   Start Kubernetes events watcher.
  --timer                         Start Timer.
  --mqt                           Start message queue trigger.
  --mqt_keda					  Start message queue trigger of kind KEDA
  --builderMgr                    Start builder manager.
  --version                       Print version information`

func main() {
	mgr := manager.New()
	defer mgr.Wait()

	// Set up command line parsing
	args := setupCommandLineArgs()

	// Handle version request specially - exit after printing
	if args.showVersion {
		fmt.Printf("Fission Bundle Version: %s\n", info.BuildInfo().String())
		os.Exit(0)
	}

	// Initialize logger
	logger := loggerfactory.GetLogger()
	defer func() {
		// https://github.com/uber-go/zap/issues/328
		_ = logger.Sync()
	}()

	// Set up signal handling for graceful shutdown
	ctx := signals.SetupSignalHandler()

	// Enable profiling if configured
	profile.ProfileIfEnabled(ctx, logger, mgr)

	// Initialize OpenTelemetry
	serviceName := getServiceNameFromArgs(args)
	shutdown, err := otel.InitProvider(ctx, logger, serviceName)
	if err != nil {
		logger.Error("error initializing provider for OTLP", zap.Error(err))
		return
	}
	if shutdown != nil {
		defer shutdown(ctx)
	}

	// Initialize client generator
	clientGen := crd.NewClientGenerator()

	// Start the appropriate service based on command line arguments
	startRequestedService(ctx, args, clientGen, logger, mgr)

	<-ctx.Done()
	logger.Error("exiting")
}

// setupCommandLineArgs parses command line arguments and returns them
func setupCommandLineArgs() *CommandLineArgs {
	args := &CommandLineArgs{}

	// Override the default usage function
	flag.Usage = func() {
		fmt.Println(usageText)
	}

	// Tell glog to log into STDERR
	flag.Set("logtostderr", "true") //nolint: errcheck

	// Define flags
	flag.BoolVar(&args.canaryConfig, "canaryConfig", false, "Start canary config server")
	flag.BoolVar(&args.kubewatcher, "kubewatcher", false, "Start Kubernetes events watcher")
	flag.BoolVar(&args.timer, "timer", false, "Start Timer")
	flag.BoolVar(&args.mqt, "mqt", false, "Start message queue trigger")
	flag.BoolVar(&args.mqt_keda, "mqt_keda", false, "Start message queue trigger of kind KEDA")
	flag.BoolVar(&args.builderMgr, "builderMgr", false, "Start builder manager")
	flag.BoolVar(&args.showVersion, "version", false, "Print version information")
	flag.BoolVar(&args.logger, "logger", false, "Start logger")

	// Port flags
	flag.IntVar(&args.webhookPort, "webhookPort", 0, "Port that the webhook should listen on")
	flag.IntVar(&args.routerPort, "routerPort", 0, "Port that the router should listen on")
	flag.IntVar(&args.executorPort, "executorPort", 0, "Port that the executor should listen on")
	flag.IntVar(&args.storageServicePort, "storageServicePort", 0, "Port that the storage service should listen on")

	// URL flags
	flag.StringVar(&args.executorUrl, "executorUrl", "http://executor.fission", "Executor URL")
	flag.StringVar(&args.routerUrl, "routerUrl", "http://router.fission", "Router URL")
	flag.StringVar(&args.storageSvcUrl, "storageSvcUrl", "http://storagesvc.fission", "StorageService URL")

	// Other configuration flags
	flag.StringVar(&args.storageType, "storageType", "", "Type of storage to use")

	// Parse flags
	flag.Parse()

	return args
}

// getServiceNameFromArgs determines which service is being started based on command line args
func getServiceNameFromArgs(args *CommandLineArgs) string {
	serviceName := "Fission-Unknown"

	if args.routerPort != 0 {
		serviceName = "Fission-Router"
	} else if args.executorPort != 0 {
		serviceName = "Fission-Executor"
	} else if args.kubewatcher {
		serviceName = "Fission-KubeWatcher"
	} else if args.timer {
		serviceName = "Fission-Timer"
	} else if args.mqt {
		serviceName = "Fission-MessageQueueTrigger"
	} else if args.builderMgr {
		serviceName = "Fission-BuilderMgr"
	} else if args.storageServicePort != 0 {
		serviceName = "Fission-StorageSvc"
	} else if args.mqt_keda {
		serviceName = "Fission-Keda-MQTrigger"
	}

	return serviceName
}

// startRequestedService starts the service specified by command line arguments
func startRequestedService(ctx context.Context, args *CommandLineArgs, clientGen crd.ClientGeneratorInterface, logger *zap.Logger, mgr manager.Interface) {
	var err error

	// Start the requested service based on command line arguments
	if args.webhookPort != 0 {
		err = webhook.Start(ctx, clientGen, logger, cnwebhook.Options{
			Port: args.webhookPort,
		})
		logger.Error("webhook server exited:", zap.Error(err))
		return
	}

	if args.canaryConfig {
		err = canaryconfigmgr.StartCanaryServer(ctx, clientGen, logger, mgr, false)
		if err != nil {
			logger.Error("canary config server exited with error: ", zap.Error(err))
		}
		return
	}

	if args.routerPort != 0 {
		err = router.Start(ctx, clientGen, logger, mgr, args.routerPort, eclient.MakeClient(logger, args.executorUrl))
		if err != nil {
			logger.Error("router exited", zap.Error(err))
		}
		return
	}

	if args.executorPort != 0 {
		err = executor.StartExecutor(ctx, clientGen, logger, mgr, args.executorPort)
		if err != nil {
			logger.Error("executor exited", zap.Error(err))
		}
		return
	}

	if args.kubewatcher {
		err = kubewatcher.Start(ctx, clientGen, logger, mgr, args.routerUrl)
		if err != nil {
			logger.Error("kubewatcher exited", zap.Error(err))
		}
		return
	}

	if args.timer {
		err = timer.Start(ctx, clientGen, logger, mgr, args.routerUrl)
		if err != nil {
			logger.Error("timer exited", zap.Error(err))
		}
		return
	}

	if args.mqt {
		err = mqtrigger.Start(ctx, clientGen, logger, mgr, args.routerUrl)
		if err != nil {
			logger.Error("message queue manager exited", zap.Error(err))
		}
		return
	}

	if args.mqt_keda {
		err = mqt.StartScalerManager(ctx, clientGen, logger, mgr, args.routerUrl)
		if err != nil {
			logger.Error("mqt scaler manager exited", zap.Error(err))
		}
		return
	}

	if args.builderMgr {
		err = buildermgr.Start(ctx, clientGen, logger, mgr, args.storageSvcUrl)
		if err != nil {
			logger.Error("builder manager exited", zap.Error(err))
		}
		return
	}

	if args.logger {
		err = functionLogger.Start(ctx, clientGen, logger)
		if err != nil {
			logger.Error("logger exited", zap.Error(err))
		}
		return
	}

	if args.storageServicePort != 0 {
		startStorageService(ctx, args, clientGen, logger, mgr)
		return
	}
}

// startStorageService initializes and starts the storage service
func startStorageService(ctx context.Context, args *CommandLineArgs, clientGen crd.ClientGeneratorInterface, logger *zap.Logger, mgr manager.Interface) {
	var storage storagesvc.Storage

	if args.storageType == string(storagesvc.StorageTypeS3) {
		storage = storagesvc.NewS3Storage()
	} else if args.storageType == string(storagesvc.StorageTypeLocal) {
		storage = storagesvc.NewLocalStorage("/fission")
	}

	err := storagesvc.Start(ctx, clientGen, logger, storage, mgr, args.storageServicePort)
	if err != nil {
		logger.Error("storage service exited", zap.Error(err))
	}
}
