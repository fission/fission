// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package services

import (
	"context"
	"fmt"
	"os"

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
	"github.com/fission/fission/pkg/utils/manager"
	"github.com/fission/fission/pkg/webhook"
	"github.com/fission/fission/test/e2e/framework"
)

func StartServices(ctx context.Context, f *framework.Framework, mgr manager.Interface) error {
	os.Setenv("DEBUG_ENV", "true")
	// The executor and buildermgr run under controller-runtime Managers whose
	// metrics server binds hard (and buildermgr's health-probe server too;
	// the executor keeps health on its API mux), unlike the fail-soft
	// ServeMetrics the other in-process services use. In this single-process
	// harness METRICS_ADDR is shared and racy, so tell them to bind ephemeral
	// ports. Set once and never mutated, so their goroutines read it
	// deterministically.
	os.Setenv("FISSION_TEST_EPHEMERAL_SERVERS", "true")
	env := f.GetEnv()
	webhookPort := env.WebhookInstallOptions.LocalServingPort
	err := f.ToggleMetricAddr()
	if err != nil {
		return fmt.Errorf("error toggling metric address: %w", err)
	}
	mgr.Add(ctx, func(ctx context.Context) {
		err = webhook.Start(ctx, f.ClientGen(), f.Logger(), cnwebhook.Options{
			Port:    webhookPort,
			CertDir: env.WebhookInstallOptions.LocalServingCertDir,
		})
		if err != nil {
			f.Logger().Error(err, "error starting webhook")
			os.Exit(1)
		}
	})
	f.AddServiceInfo("webhook", framework.ServiceInfo{Port: webhookPort})

	executorPort, err := utils.FindFreePort()
	if err != nil {
		return fmt.Errorf("error finding unused port: %w", err)
	}
	err = f.ToggleMetricAddr()
	if err != nil {
		return fmt.Errorf("error toggling metric address: %w", err)
	}

	// namespace settings for components
	os.Setenv("FISSION_BUILDER_NAMESPACE", "")
	os.Setenv("FISSION_FUNCTION_NAMESPACE", "")
	os.Setenv("FISSION_DEFAULT_NAMESPACE", "default")
	os.Setenv("FISSION_RESOURCE_NAMESPACES", "default")
	utils.DefaultNSResolver().DefaultNamespace = "default"
	utils.DefaultNSResolver().FissionResourceNS = map[string]string{
		"default": "default",
	}

	os.Setenv("POD_READY_TIMEOUT", "300s")
	// executor now runs under a controller-runtime Manager, so StartExecutor
	// blocks (like webhook.Start). Run it in a goroutine so the remaining
	// services still come up.
	mgr.Add(ctx, func(ctx context.Context) {
		if err := executor.StartExecutor(ctx, f.ClientGen(), f.Logger(), mgr, executorPort); err != nil {
			f.Logger().Error(err, "error starting executor")
			os.Exit(1)
		}
	})
	f.AddServiceInfo("executor", framework.ServiceInfo{Port: executorPort})

	os.Setenv("PRUNE_ENABLED", "true")
	os.Setenv("PRUNE_INTERVAL", "60")

	err = f.ToggleMetricAddr()
	if err != nil {
		return fmt.Errorf("error toggling metric address: %w", err)
	}

	err = f.ToggleMetricAddr()
	if err != nil {
		return fmt.Errorf("error toggling metric address: %w", err)
	}

	storageSvcPort, err := StartStorageSvc(ctx, f, mgr)
	if err != nil {
		return err
	}

	// buildermgr's Start blocks (controller-runtime Manager), so run it in a
	// goroutine; FISSION_TEST_EPHEMERAL_SERVERS (set at the top) makes its
	// Manager servers bind ephemeral ports.
	storageSvcURL := fmt.Sprintf("http://localhost:%d", storageSvcPort)
	mgr.Add(ctx, func(ctx context.Context) {
		if err := buildermgr.Start(ctx, f.ClientGen(), f.Logger(), mgr, storageSvcURL); err != nil {
			f.Logger().Error(err, "error starting builder manager")
			os.Exit(1)
		}
	})
	f.AddServiceInfo("buildermgr", framework.ServiceInfo{})

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
	routerPort, err := utils.FindFreePort()
	if err != nil {
		return fmt.Errorf("error finding unused port: %w", err)
	}
	err = f.ToggleMetricAddr()
	if err != nil {
		return fmt.Errorf("error toggling metric address: %w", err)
	}

	// E2E framework runs the executor in the same process as its caller,
	// so any FISSION_INTERNAL_AUTH_SECRET set on the test environment
	// flows into both ends of this client/verifier pair via
	// HMACSecretFromEnv (returns nil when unset, leaving the channel
	// unsigned).
	executor := eclient.MakeClient(f.Logger(), fmt.Sprintf("http://localhost:%d", executorPort), storagesvcClient.HMACSecretFromEnv())
	internalRouterPort, err := utils.FindFreePort()
	if err != nil {
		return fmt.Errorf("error finding unused port for router internal listener: %w", err)
	}
	// router now runs under a controller-runtime Manager, so Start blocks. Run
	// it in a goroutine so the harness can continue; FISSION_TEST_EPHEMERAL_SERVERS
	// (set at the top) makes its Manager metrics server bind an ephemeral port.
	mgr.Add(ctx, func(ctx context.Context) {
		if err := router.Start(ctx, f.ClientGen(), f.Logger(), mgr, routerPort, internalRouterPort, executor); err != nil {
			f.Logger().Error(err, "error starting router")
			os.Exit(1)
		}
	})
	f.AddServiceInfo("router", framework.ServiceInfo{Port: routerPort})
	f.AddServiceInfo("router-internal", framework.ServiceInfo{Port: internalRouterPort})
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
	mgr.Add(ctx, func(ctx context.Context) {
		if err := timer.Start(ctx, f.ClientGen(), f.Logger(), mgr, internalRouterURL); err != nil {
			f.Logger().Error(err, "error starting timer")
			os.Exit(1)
		}
	})
	f.AddServiceInfo("timer", framework.ServiceInfo{})

	mgr.Add(ctx, func(ctx context.Context) {
		if err := mqtrigger.StartScalerManager(ctx, f.ClientGen(), f.Logger(), mgr, internalRouterURL); err != nil {
			f.Logger().Error(err, "error starting mqt scaler manager")
			os.Exit(1)
		}
	})
	f.AddServiceInfo("mqtrigger-keda", framework.ServiceInfo{})

	mgr.Add(ctx, func(ctx context.Context) {
		if err := kubewatcher.Start(ctx, f.ClientGen(), f.Logger(), mgr, internalRouterURL); err != nil {
			f.Logger().Error(err, "error starting kubewatcher")
			os.Exit(1)
		}
	})
	f.AddServiceInfo("kubewatcher", framework.ServiceInfo{})

	return nil
}

func StartStorageSvc(ctx context.Context, f *framework.Framework, mgr manager.Interface) (storageSvcPort int, err error) {
	storageDir, err := os.MkdirTemp("/tmp", "storagesvc")
	if err != nil {
		return 0, fmt.Errorf("error creating temp directory: %w", err)
	}

	storageSvcPort, err = utils.FindFreePort()
	if err != nil {
		return storageSvcPort, fmt.Errorf("error finding unused port: %w", err)
	}

	err = storagesvc.Start(ctx, f.ClientGen(), f.Logger(), storagesvc.NewLocalStorage(storageDir), mgr, storageSvcPort)
	if err != nil {
		return storageSvcPort, fmt.Errorf("error starting storage service: %w", err)
	}
	f.AddServiceInfo("storagesvc", framework.ServiceInfo{Port: storageSvcPort})
	storagesvcURL, err := f.GetServiceURL("storagesvc")
	if err != nil {
		return storageSvcPort, fmt.Errorf("error getting storage service URL: %w", err)
	}
	os.Setenv("FISSION_STORAGESVC_URL", storagesvcURL)

	return storageSvcPort, nil
}
