package services

import (
	"context"
	"fmt"
	"os"

	"go.uber.org/zap"
	cnwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/fission/fission/pkg/buildermgr"
	"github.com/fission/fission/pkg/executor"
	eclient "github.com/fission/fission/pkg/executor/client"
	"github.com/fission/fission/pkg/kubewatcher"
	"github.com/fission/fission/pkg/mqtrigger"
	"github.com/fission/fission/pkg/router"
	"github.com/fission/fission/pkg/storagesvc"
	"github.com/fission/fission/pkg/timer"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/manager"
	"github.com/fission/fission/pkg/webhook"
	"github.com/fission/fission/test/e2e/framework"
)

func StartServices(ctx context.Context, f *framework.Framework, mgr manager.Interface) error {
	os.Setenv("DEBUG_ENV", "true")
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
			f.Logger().Fatal("error starting webhook", zap.Error(err))
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
	err = executor.StartExecutor(ctx, f.ClientGen(), f.Logger(), mgr, executorPort)
	if err != nil {
		return fmt.Errorf("error starting executor: %w", err)
	}
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

	err = buildermgr.Start(ctx, f.ClientGen(), f.Logger(), mgr, fmt.Sprintf("http://localhost:%d", storageSvcPort))
	if err != nil {
		return fmt.Errorf("error starting builder manager: %w", err)
	}
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

	executor := eclient.MakeClient(f.Logger(), fmt.Sprintf("http://localhost:%d", executorPort))
	err = router.Start(ctx, f.ClientGen(), f.Logger(), mgr, routerPort, executor)
	if err != nil {
		return fmt.Errorf("error starting router: %w", err)
	}
	f.AddServiceInfo("router", framework.ServiceInfo{Port: routerPort})
	routerURL, err := f.GetServiceURL("router")
	if err != nil {
		return fmt.Errorf("error getting router URL: %w", err)
	}
	os.Setenv("FISSION_ROUTER_URL", routerURL)

	err = timer.Start(ctx, f.ClientGen(), f.Logger(), mgr, routerURL)
	if err != nil {
		return fmt.Errorf("error starting timer: %w", err)
	}
	f.AddServiceInfo("timer", framework.ServiceInfo{})

	err = mqtrigger.StartScalerManager(ctx, f.ClientGen(), f.Logger(), mgr, routerURL)
	if err != nil {
		return fmt.Errorf("error starting mqt scaler manager: %w", err)
	}
	f.AddServiceInfo("mqtrigger-keda", framework.ServiceInfo{})

	err = kubewatcher.Start(ctx, f.ClientGen(), f.Logger(), mgr, routerURL)
	if err != nil {
		return fmt.Errorf("error starting kubewatcher: %w", err)
	}
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
