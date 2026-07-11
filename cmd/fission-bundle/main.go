// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	ctrl "sigs.k8s.io/controller-runtime"
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
	"github.com/fission/fission/pkg/mcp"
	mqt "github.com/fission/fission/pkg/mqtrigger"
	"github.com/fission/fission/pkg/router"
	"github.com/fission/fission/pkg/storagesvc"
	storagesvcClient "github.com/fission/fission/pkg/storagesvc/client"
	"github.com/fission/fission/pkg/tenant"
	"github.com/fission/fission/pkg/timer"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"github.com/fission/fission/pkg/utils/otel"
	"github.com/fission/fission/pkg/utils/profile"
	"github.com/fission/fission/pkg/webhook"

	"github.com/fission/fission/pkg/svcinfo"
)

// Command line arguments
type CommandLineArgs struct {
	// Flags
	canaryConfig     bool
	kubewatcher      bool
	timer            bool
	mqt              bool
	mqt_keda         bool
	builderMgr       bool
	showVersion      bool
	tenantController bool
	seedTenants      bool

	// Port values
	webhookPort        int
	routerPort         int
	routerInternalPort int
	executorPort       int
	storageServicePort int
	mcpPort            int

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
  --tenantController              Start the multi-namespace tenant lifecycle controller.
  --version                       Print version information`

func main() {
	mgr := &errgroup.Group{}
	defer func() { _ = mgr.Wait() }()

	// Set up command line parsing
	args := setupCommandLineArgs()

	// Handle version request specially - exit after printing
	if args.showVersion {
		fmt.Printf("Fission Bundle Version: %s\n", info.BuildInfo().String())
		os.Exit(0)
	}

	// Initialize logger
	logger := loggerfactory.GetLogger()

	// GOMAXPROCS is left to the runtime: Go ≥1.25 derives it from the cgroup
	// CPU quota (including on in-place resize); automaxprocs would regress that.

	// ctrl.SetLogger targets controller-runtime's process-global logger. Set it
	// once here, before any subsystem's Start builds its manager, so every
	// subsystem routes controller-runtime's own logs (cache-sync, reconcile,
	// leader-election) through our logger instead of dropping them with a one-off
	// "log.SetLogger was never called" stack trace.
	ctrl.SetLogger(logger.WithName("controller-runtime"))

	// Set up signal handling for graceful shutdown
	ctx := signals.SetupSignalHandler()

	// Enable profiling if configured
	profile.ProfileIfEnabled(ctx, logger, mgr)

	// Initialize OpenTelemetry
	serviceName := getServiceNameFromArgs(args)
	shutdown, err := otel.InitProvider(ctx, logger, serviceName)
	if err != nil {
		logger.Error(err, "error initializing provider for OTLP")
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
	logger.Info("exiting")
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
	flag.BoolVar(&args.tenantController, "tenantController", false, "Start the multi-namespace tenant lifecycle controller")
	flag.BoolVar(&args.seedTenants, "seedTenants", false, "Seed FissionTenant CRs from the env namespace config, then exit (migration hook)")
	flag.BoolVar(&args.showVersion, "version", false, "Print version information")

	// Port flags
	flag.IntVar(&args.webhookPort, "webhookPort", 0, "Port that the webhook should listen on")
	flag.IntVar(&args.routerPort, "routerPort", 0, "Port that the router should listen on")
	flag.IntVar(&args.routerInternalPort, "routerInternalPort", svcinfo.PortRouterInternal, "Port for the router internal listener that serves /fission-function/<ns>/<name>")
	flag.IntVar(&args.executorPort, "executorPort", 0, "Port that the executor should listen on")
	flag.IntVar(&args.storageServicePort, "storageServicePort", 0, "Port that the storage service should listen on")
	flag.IntVar(&args.mcpPort, "mcpPort", 0, "Port that the MCP tool server should listen on")

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
	} else if args.mcpPort != 0 {
		serviceName = "Fission-MCP"
	} else if args.tenantController {
		serviceName = "Fission-TenantController"
	}

	return serviceName
}

// startRequestedService starts the service specified by command line arguments
func startRequestedService(ctx context.Context, args *CommandLineArgs, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr *errgroup.Group) {
	var err error

	// One-shot migration hook: seed FissionTenant CRs from the env namespace
	// config and exit. Run as a Helm post-install/post-upgrade Job; idempotent.
	if args.seedTenants {
		fissionClient, ferr := clientGen.GetFissionClient()
		if ferr != nil {
			logger.Error(ferr, "tenant seeding: get fission client")
			os.Exit(1)
		}
		if serr := tenant.SeedTenants(ctx, fissionClient, utils.DefaultNSResolver(), logger); serr != nil {
			logger.Error(serr, "tenant seeding failed")
			os.Exit(1)
		}
		logger.Info("tenant seeding complete")
		os.Exit(0)
	}

	// Start the requested service based on command line arguments
	if args.webhookPort != 0 {
		err = webhook.Start(ctx, clientGen, logger, cnwebhook.Options{
			Port: args.webhookPort,
		})
		logger.Error(err, "webhook server exited:")
		return
	}

	if args.canaryConfig {
		err = canaryconfigmgr.StartCanaryServer(ctx, clientGen, logger, mgr, false)
		if err != nil {
			logger.Error(err, "canary config server exited with error: ")
		}
		return
	}

	if args.routerPort != 0 {
		err = router.Start(ctx, clientGen, logger, mgr, args.routerPort, args.routerInternalPort, eclient.MakeClient(logger, args.executorUrl, storagesvcClient.HMACSecretFromEnv()))
		if err != nil {
			logger.Error(err, "router exited")
		}
		return
	}

	if args.executorPort != 0 {
		err = executor.StartExecutor(ctx, clientGen, logger, mgr, args.executorPort)
		if err != nil {
			logger.Error(err, "executor exited")
		}
		return
	}

	// ROUTER_INTERNAL_URL (set by the chart on internal-publisher pods)
	// overrides the legacy --routerUrl flag for kubewatcher / timer /
	// mqtrigger / mqt_keda — they all publish to /fission-function/...,
	// which after GHSA-3g33-6vg6-27m8 lives only on the router's
	// internal listener (port 8889 on svc/router-internal).
	publishURL := args.routerUrl
	if internal := os.Getenv("ROUTER_INTERNAL_URL"); internal != "" {
		publishURL = internal
	}

	if args.kubewatcher {
		err = kubewatcher.Start(ctx, clientGen, logger, mgr, publishURL)
		if err != nil {
			logger.Error(err, "kubewatcher exited")
		}
		return
	}

	if args.timer {
		err = timer.Start(ctx, clientGen, logger, mgr, publishURL)
		if err != nil {
			logger.Error(err, "timer exited")
		}
		return
	}

	if args.mqt {
		err = mqtrigger.Start(ctx, clientGen, logger, mgr, publishURL)
		if err != nil {
			logger.Error(err, "message queue manager exited")
		}
		return
	}

	if args.mqt_keda {
		err = mqt.StartScalerManager(ctx, clientGen, logger, mgr, publishURL)
		if err != nil {
			logger.Error(err, "mqt scaler manager exited")
		}
		return
	}

	if args.mcpPort != 0 {
		// The MCP server proxies tools/call to /fission-function/... on the
		// router internal listener, so it takes the same resolved publishURL
		// (ROUTER_INTERNAL_URL) as kubewatcher/timer/mqt.
		err = mcp.Start(ctx, clientGen, logger, mgr, args.mcpPort, publishURL)
		if err != nil {
			logger.Error(err, "mcp server exited")
		}
		return
	}

	if args.tenantController {
		err = tenant.Start(ctx, clientGen, logger, mgr)
		if err != nil {
			logger.Error(err, "tenant controller exited")
		}
		return
	}

	if args.builderMgr {
		err = buildermgr.Start(ctx, clientGen, logger, mgr, args.storageSvcUrl)
		if err != nil {
			logger.Error(err, "builder manager exited")
		}
		return
	}

	if args.storageServicePort != 0 {
		startStorageService(ctx, args, clientGen, logger, mgr)
		return
	}
}

// startStorageService initializes and starts the storage service
func startStorageService(ctx context.Context, args *CommandLineArgs, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr *errgroup.Group) {
	var storage storagesvc.Storage

	if args.storageType == string(storagesvc.StorageTypeS3) {
		storage = storagesvc.NewS3Storage()
	} else if args.storageType == string(storagesvc.StorageTypeLocal) {
		storage = storagesvc.NewLocalStorage("/fission")
	}

	err := storagesvc.Start(ctx, clientGen, logger, storage, mgr, args.storageServicePort)
	if err != nil {
		logger.Error(err, "storage service exited")
	}
}
