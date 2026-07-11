// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package services

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/sync/errgroup"
	ctrl "sigs.k8s.io/controller-runtime"
	cnwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/fission/fission/pkg/buildermgr"
	"github.com/fission/fission/pkg/executor"
	eclient "github.com/fission/fission/pkg/executor/client"
	"github.com/fission/fission/pkg/kubewatcher"
	"github.com/fission/fission/pkg/mqtrigger"
	"github.com/fission/fission/pkg/router"
	"github.com/fission/fission/pkg/storagesvc"
	storagesvcClient "github.com/fission/fission/pkg/storagesvc/client"
	"github.com/fission/fission/pkg/timer"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/webhook"
	"github.com/fission/fission/test/e2e/framework"
)

func StartServices(ctx context.Context, f *framework.Framework, mgr *errgroup.Group) error {
	// This harness runs several controller-runtime managers (webhook, executor,
	// router, ...) in ONE process, so set controller-runtime's global logger once
	// here — the entrypoint — rather than in each subsystem's Start (which would
	// re-set the global per manager). Mirrors cmd/fission-bundle main().
	ctrl.SetLogger(f.Logger().WithName("controller-runtime"))

	// runService runs a blocking subsystem Start in the harness errgroup. A
	// subsystem that fails to come up must abort the run immediately (os.Exit).
	// But once ctx is cancelled the run is tearing down, and a Start returning
	// then is shutdown noise — e.g. a controller-runtime manager whose runnables
	// exceed the 30s graceful-shutdown grace period under CI load — so it must
	// not fail the test. The ctx.Err() guard also drops a data race on the
	// shared err that the inline goroutines used to write.
	runService := func(name string, start func() error) {
		mgr.Go(func() error {
			if err := start(); err != nil && ctx.Err() == nil {
				f.Logger().Error(err, "error starting "+name)
				os.Exit(1)
			}
			return nil
		})
	}

	os.Setenv("DEBUG_ENV", "true")
	// Every metrics/health server binds an ephemeral port: all consumers
	// resolve their bind through httpserver.BindAddrFromEnv, and "0" makes
	// the kernel assign per-listener — no pre-picked ports, no collisions.
	os.Setenv("METRICS_ADDR", "0")
	os.Setenv("HEALTH_PROBE_ADDR", "0")
	env := f.GetEnv()
	webhookPort := env.WebhookInstallOptions.LocalServingPort
	runService("webhook", func() error {
		return webhook.Start(ctx, f.ClientGen(), f.Logger(), cnwebhook.Options{
			Port:    webhookPort,
			CertDir: env.WebhookInstallOptions.LocalServingCertDir,
		})
	})
	// Webhook readiness must prove a completed TLS handshake, not just a TCP
	// accept — envtest's CRD installs go through this server immediately after.
	if err := f.RegisterService("webhook", webhookPort, framework.WithTLSReadyCheck()); err != nil {
		return err
	}

	// namespace settings for components
	os.Setenv("FISSION_BUILDER_NAMESPACE", "")
	os.Setenv("FISSION_FUNCTION_NAMESPACE", "")
	os.Setenv("FISSION_DEFAULT_NAMESPACE", "default")
	os.Setenv("FISSION_RESOURCE_NAMESPACES", "default")
	utils.DefaultNSResolver().DefaultNamespace = "default"
	utils.DefaultNSResolver().SetTenants(map[string]string{
		"default": "default",
	})

	os.Setenv("POD_READY_TIMEOUT", "300s")
	// executor now runs under a controller-runtime Manager, so its Start
	// blocks (like webhook.Start). Run it in a goroutine so the remaining
	// services still come up. The harness binds each service's listener
	// itself (kernel-assigned port) and injects it, so no port is ever
	// pre-picked.
	executorListener, err := f.ListenAndRegister("executor")
	if err != nil {
		return err
	}
	runService("executor", func() error {
		return executor.StartExecutorWithOptions(ctx, f.ClientGen(), f.Logger(), mgr, executor.Options{Listener: executorListener})
	})

	os.Setenv("PRUNE_ENABLED", "true")
	os.Setenv("PRUNE_INTERVAL", "60")

	if err := StartStorageSvc(ctx, f, mgr); err != nil {
		return err
	}

	// buildermgr's Start blocks (controller-runtime Manager), so run it in a
	// goroutine; METRICS_ADDR/HEALTH_PROBE_ADDR=0 (set at the top) make its
	// Manager servers bind ephemeral ports.
	storageSvcURL, err := f.GetServiceURL("storagesvc")
	if err != nil {
		return err
	}
	runService("builder manager", func() error {
		return buildermgr.Start(ctx, f.ClientGen(), f.Logger(), mgr, storageSvcURL)
	})

	os.Setenv("ROUTER_ROUND_TRIP_TIMEOUT", "50ms")
	os.Setenv("ROUTER_ROUNDTRIP_TIMEOUT_EXPONENT", "2")
	os.Setenv("ROUTER_ROUND_TRIP_KEEP_ALIVE_TIME", "30s")
	os.Setenv("ROUTER_ROUND_TRIP_DISABLE_KEEP_ALIVE", "true")
	os.Setenv("ROUTER_ROUND_TRIP_MAX_RETRIES", "10")
	os.Setenv("ROUTER_SVC_ADDRESS_MAX_RETRIES", "5")
	os.Setenv("ROUTER_SVC_ADDRESS_UPDATE_TIMEOUT", "30s")
	os.Setenv("ROUTER_UNTAP_SERVICE_TIMEOUT", "3600s")
	os.Setenv("USE_ENCODED_PATH", "false")
	os.Setenv("DISPLAY_ACCESS_LOG", "true")
	// os.Setenv("DEBUG_ENV", "false")
	// E2E framework runs the executor in the same process as its caller,
	// so any FISSION_INTERNAL_AUTH_SECRET set on the test environment
	// flows into both ends of this client/verifier pair via
	// HMACSecretFromEnv (returns nil when unset, leaving the channel
	// unsigned).
	executorURL, err := f.GetServiceURL("executor")
	if err != nil {
		return err
	}
	executorClient := eclient.MakeClient(f.Logger(), executorURL, storagesvcClient.HMACSecretFromEnv())
	routerListener, err := f.ListenAndRegister("router")
	if err != nil {
		return err
	}
	internalListener, err := f.ListenAndRegister("router-internal")
	if err != nil {
		return err
	}
	// router now runs under a controller-runtime Manager, so its Start
	// blocks. Run it in a goroutine so the harness can continue.
	runService("router", func() error {
		return router.StartWithOptions(ctx, f.ClientGen(), f.Logger(), mgr, router.Options{
			Listener:         routerListener,
			InternalListener: internalListener,
			Executor:         executorClient,
		})
	})
	routerURL, err := f.GetServiceURL("router")
	if err != nil {
		return fmt.Errorf("error getting router URL: %w", err)
	}
	os.Setenv("FISSION_ROUTER_URL", routerURL)
	// Point internal callers (timer / kubewatcher / mqtrigger) at the
	// router internal listener so /fission-function/<ns>/<name> reaches
	// the right port post-GHSA-3g33-6vg6-27m8.
	internalRouterURL, err := f.GetServiceURL("router-internal")
	if err != nil {
		return fmt.Errorf("error getting router internal URL: %w", err)
	}
	os.Setenv("ROUTER_INTERNAL_URL", internalRouterURL)

	// timer / kubewatcher / mqtrigger publish to /fission-function/...,
	// which after GHSA-3g33-6vg6-27m8 lives only on the router internal
	// listener. The fission-bundle entrypoint resolves
	// ROUTER_INTERNAL_URL from the env before forwarding into these
	// Start functions; this in-process harness has to do the same
	// resolution explicitly because it bypasses the bundle.
	// timer, mqt_keda and kubewatcher now run under controller-runtime Managers,
	// so their Start funcs block until ctx is cancelled. Run each in a goroutine
	// so the harness can continue.
	runService("timer", func() error {
		return timer.Start(ctx, f.ClientGen(), f.Logger(), mgr, internalRouterURL)
	})

	runService("mqt scaler manager", func() error {
		return mqtrigger.StartScalerManager(ctx, f.ClientGen(), f.Logger(), mgr, internalRouterURL)
	})

	runService("kubewatcher", func() error {
		return kubewatcher.Start(ctx, f.ClientGen(), f.Logger(), mgr, internalRouterURL)
	})

	return nil
}

func StartStorageSvc(ctx context.Context, f *framework.Framework, mgr *errgroup.Group) error {
	storageDir, err := os.MkdirTemp("/tmp", "storagesvc")
	if err != nil {
		return fmt.Errorf("error creating temp directory: %w", err)
	}

	listener, err := f.ListenAndRegister("storagesvc")
	if err != nil {
		return err
	}
	err = storagesvc.StartWithOptions(ctx, f.ClientGen(), f.Logger(), storagesvc.NewLocalStorage(storageDir), mgr, storagesvc.Options{Listener: listener})
	if err != nil {
		return fmt.Errorf("error starting storage service: %w", err)
	}
	storagesvcURL, err := f.GetServiceURL("storagesvc")
	if err != nil {
		return fmt.Errorf("error getting storage service URL: %w", err)
	}
	os.Setenv("FISSION_STORAGESVC_URL", storagesvcURL)

	return nil
}
