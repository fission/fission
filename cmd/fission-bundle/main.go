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
	"github.com/fission/fission/pkg/svcinfo"
	"github.com/fission/fission/pkg/tenant"
	"github.com/fission/fission/pkg/timer"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"github.com/fission/fission/pkg/utils/otel"
	"github.com/fission/fission/pkg/utils/profile"
	"github.com/fission/fission/pkg/webhook"
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

	// URL values — empty means "not set": the resolver derives the
	// in-cluster default from POD_NAMESPACE (see svcinfo.AddressResolver)
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
	flag.StringVar(&args.executorUrl, "executorUrl", "", "Executor URL (default http://executor.<POD_NAMESPACE>)")
	flag.StringVar(&args.routerUrl, "routerUrl", "", "Router URL (default http://router.<POD_NAMESPACE>)")
	flag.StringVar(&args.storageSvcUrl, "storageSvcUrl", "", "StorageService URL (default http://storagesvc.<POD_NAMESPACE>)")

	// Other configuration flags
	flag.StringVar(&args.storageType, "storageType", "", "Type of storage to use")

	// Parse flags
	flag.Parse()

	return args
}

// bundleService describes one dispatchable fission-bundle subsystem: its
// OTEL service name, its selection predicate over the parsed flags, and its
// runner. Both the dispatch and the OTEL service name derive from this one
// table, in order (first match wins) — adding a subsystem is one flag plus
// one entry here.
type bundleService struct {
	name     string
	selected func(*CommandLineArgs) bool
	run      func(ctx context.Context, deps bundleDeps) error
	// oneShot marks a job-style entry (Helm Job): the dispatcher exits the
	// process with run's status instead of waiting for ctx like a service.
	oneShot bool
}

// bundleDeps carries everything a subsystem runner needs.
type bundleDeps struct {
	args      *CommandLineArgs
	clientGen crd.ClientGeneratorInterface
	logger    logr.Logger
	mgr       *errgroup.Group
	resolver  svcinfo.AddressResolver
}

// bundleServices returns the dispatch table in precedence order.
func bundleServices() []bundleService {
	return []bundleService{
		{
			// One-shot migration hook: seed FissionTenant CRs from the env
			// namespace config and exit. Run as a Helm
			// post-install/post-upgrade Job; idempotent.
			name:     "Fission-SeedTenants",
			selected: func(a *CommandLineArgs) bool { return a.seedTenants },
			oneShot:  true,
			run: func(ctx context.Context, d bundleDeps) error {
				fissionClient, err := d.clientGen.GetFissionClient()
				if err != nil {
					return fmt.Errorf("tenant seeding: get fission client: %w", err)
				}
				if err := tenant.SeedTenants(ctx, fissionClient, utils.DefaultNSResolver(), d.logger); err != nil {
					return fmt.Errorf("tenant seeding failed: %w", err)
				}
				d.logger.Info("tenant seeding complete")
				return nil
			},
		},
		{
			name:     "Fission-Webhook",
			selected: func(a *CommandLineArgs) bool { return a.webhookPort != 0 },
			run: func(ctx context.Context, d bundleDeps) error {
				return webhook.Start(ctx, d.clientGen, d.logger, cnwebhook.Options{Port: d.args.webhookPort})
			},
		},
		{
			name:     "Fission-CanaryConfig",
			selected: func(a *CommandLineArgs) bool { return a.canaryConfig },
			run: func(ctx context.Context, d bundleDeps) error {
				return canaryconfigmgr.StartCanaryServer(ctx, d.clientGen, d.logger, d.mgr, false)
			},
		},
		{
			name:     "Fission-Router",
			selected: func(a *CommandLineArgs) bool { return a.routerPort != 0 },
			run: func(ctx context.Context, d bundleDeps) error {
				executorClient := eclient.MakeClient(d.logger, d.resolver.ExecutorURL(), storagesvcClient.HMACSecretFromEnv())
				return router.Start(ctx, d.clientGen, d.logger, d.mgr, router.Options{
					Port: d.args.routerPort, InternalPort: d.args.routerInternalPort, Executor: executorClient,
				})
			},
		},
		{
			name:     "Fission-Executor",
			selected: func(a *CommandLineArgs) bool { return a.executorPort != 0 },
			run: func(ctx context.Context, d bundleDeps) error {
				return executor.StartExecutor(ctx, d.clientGen, d.logger, d.mgr, executor.Options{Port: d.args.executorPort})
			},
		},
		// The publishers below target the router's internal listener; the
		// resolver applies the ROUTER_INTERNAL_URL-beats---routerUrl
		// precedence (see svcinfo.AddressResolver).
		{
			name:     "Fission-KubeWatcher",
			selected: func(a *CommandLineArgs) bool { return a.kubewatcher },
			run: func(ctx context.Context, d bundleDeps) error {
				return kubewatcher.Start(ctx, d.clientGen, d.logger, d.mgr, d.resolver.RouterInternalURL())
			},
		},
		{
			name:     "Fission-Timer",
			selected: func(a *CommandLineArgs) bool { return a.timer },
			run: func(ctx context.Context, d bundleDeps) error {
				return timer.Start(ctx, d.clientGen, d.logger, d.mgr, d.resolver.RouterInternalURL())
			},
		},
		{
			name:     "Fission-MessageQueueTrigger",
			selected: func(a *CommandLineArgs) bool { return a.mqt },
			run: func(ctx context.Context, d bundleDeps) error {
				return mqtrigger.Start(ctx, d.clientGen, d.logger, d.mgr, d.resolver.RouterInternalURL())
			},
		},
		{
			name:     "Fission-Keda-MQTrigger",
			selected: func(a *CommandLineArgs) bool { return a.mqt_keda },
			run: func(ctx context.Context, d bundleDeps) error {
				return mqt.StartScalerManager(ctx, d.clientGen, d.logger, d.mgr, d.resolver.RouterInternalURL())
			},
		},
		{
			// The MCP server proxies tools/call to /fission-function/... on
			// the router internal listener, so it takes the same resolved
			// URL as the other publishers.
			name:     "Fission-MCP",
			selected: func(a *CommandLineArgs) bool { return a.mcpPort != 0 },
			run: func(ctx context.Context, d bundleDeps) error {
				return mcp.Start(ctx, d.clientGen, d.logger, d.mgr, mcp.Options{Port: d.args.mcpPort, RouterInternalURL: d.resolver.RouterInternalURL()})
			},
		},
		{
			name:     "Fission-TenantController",
			selected: func(a *CommandLineArgs) bool { return a.tenantController },
			run: func(ctx context.Context, d bundleDeps) error {
				return tenant.Start(ctx, d.clientGen, d.logger, d.mgr)
			},
		},
		{
			name:     "Fission-BuilderMgr",
			selected: func(a *CommandLineArgs) bool { return a.builderMgr },
			run: func(ctx context.Context, d bundleDeps) error {
				return buildermgr.Start(ctx, d.clientGen, d.logger, d.mgr, d.resolver.StorageSvcURL())
			},
		},
		{
			name:     "Fission-StorageSvc",
			selected: func(a *CommandLineArgs) bool { return a.storageServicePort != 0 },
			run: func(ctx context.Context, d bundleDeps) error {
				return startStorageService(ctx, d.args, d.clientGen, d.logger, d.mgr)
			},
		},
	}
}

// selectBundleService returns the first table entry whose predicate matches,
// or nil when no subsystem flag was given.
func selectBundleService(args *CommandLineArgs) *bundleService {
	svcs := bundleServices()
	for i := range svcs {
		if svcs[i].selected(args) {
			return &svcs[i]
		}
	}
	return nil
}

// getServiceNameFromArgs determines the OTEL service name from the dispatch
// table, so the name and the dispatch can never drift apart.
func getServiceNameFromArgs(args *CommandLineArgs) string {
	if svc := selectBundleService(args); svc != nil {
		return svc.name
	}
	return "Fission-Unknown"
}

// startRequestedService starts the service selected by the command line
// arguments via the dispatch table.
func startRequestedService(ctx context.Context, args *CommandLineArgs, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr *errgroup.Group) {
	svc := selectBundleService(args)
	if svc == nil {
		return
	}
	// One resolution seam for every sibling-service URL — see
	// svcinfo.AddressResolver for the documented precedence.
	deps := bundleDeps{
		args:      args,
		clientGen: clientGen,
		logger:    logger,
		mgr:       mgr,
		resolver: svcinfo.NewEnvResolver(svcinfo.FlagValues{
			ExecutorURL:   args.executorUrl,
			RouterURL:     args.routerUrl,
			StorageSvcURL: args.storageSvcUrl,
		}),
	}
	err := svc.run(ctx, deps)
	if svc.oneShot {
		// Job-style entries (Helm Jobs) must exit with a status instead of
		// falling through to main's long-running service wait.
		if err != nil {
			logger.Error(err, "job failed", "service", svc.name)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if err != nil {
		logger.Error(err, "service exited", "service", svc.name)
	}
}

// startStorageService initializes and starts the storage service
func startStorageService(ctx context.Context, args *CommandLineArgs, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr *errgroup.Group) error {
	var storage storagesvc.Storage

	if args.storageType == string(storagesvc.StorageTypeS3) {
		storage = storagesvc.NewS3Storage()
	} else if args.storageType == string(storagesvc.StorageTypeLocal) {
		storage = storagesvc.NewLocalStorage("/fission")
	}

	return storagesvc.Start(ctx, clientGen, logger, storage, mgr, storagesvc.Options{Port: args.storageServicePort})
}
