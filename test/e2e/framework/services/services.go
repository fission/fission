package services

import (
	"context"
	"fmt"
	"os"

	"github.com/fission/fission/pkg/buildermgr"
	"github.com/fission/fission/pkg/executor"
	"github.com/fission/fission/pkg/router"
	"github.com/fission/fission/pkg/storagesvc"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/test/e2e/framework"
)

func StartServices(ctx context.Context, f *framework.Framework) error {
	executorPort, err := utils.FindFreePort()
	if err != nil {
		return fmt.Errorf("error finding unused port: %v", err)
	}
	err = f.ToggleMetricAddr()
	if err != nil {
		return fmt.Errorf("error toggling metric address: %v", err)
	}
	err = executor.StartExecutor(ctx, f.ClientGen(), f.Logger(), executorPort)
	if err != nil {
		return fmt.Errorf("error starting executor: %v", err)
	}
	f.ServiceInfo["executor"] = framework.ServiceInfo{
		Port: executorPort,
	}

	os.Setenv("PRUNE_ENABLED", "true")
	os.Setenv("PRUNE_INTERVAL", "60")
	storageDir, err := os.MkdirTemp("/tmp", "storagesvc")
	if err != nil {
		return fmt.Errorf("error creating temp directory: %v", err)
	}

	storageSvcPort, err := utils.FindFreePort()
	if err != nil {
		return fmt.Errorf("error finding unused port: %v", err)
	}
	err = f.ToggleMetricAddr()
	if err != nil {
		return fmt.Errorf("error toggling metric address: %v", err)
	}
	err = storagesvc.Start(ctx, f.ClientGen(), f.Logger(), storagesvc.NewLocalStorage(storageDir), storageSvcPort)
	if err != nil {
		return fmt.Errorf("error starting storage service: %v", err)
	}
	f.ServiceInfo["storagesvc"] = framework.ServiceInfo{
		Port: storageSvcPort,
	}
	err = f.ToggleMetricAddr()
	if err != nil {
		return fmt.Errorf("error toggling metric address: %v", err)
	}
	err = buildermgr.Start(ctx, f.ClientGen(), f.Logger(), fmt.Sprintf("http://localhost:%d", storageSvcPort))
	if err != nil {
		return fmt.Errorf("error starting builder manager: %v", err)
	}
	f.ServiceInfo["buildermgr"] = framework.ServiceInfo{}

	os.Setenv("ROUTER_ROUND_TRIP_TIMEOUT", "50ms")
	os.Setenv("ROUTER_ROUNDTRIP_TIMEOUT_EXPONENT", "2")
	os.Setenv("ROUTER_ROUND_TRIP_KEEP_ALIVE_TIME", "30s")
	os.Setenv("ROUTER_ROUND_TRIP_DISABLE_KEEP_ALIVE", "true")
	os.Setenv("ROUTER_ROUND_TRIP_MAX_RETRIES", "10")
	os.Setenv("ROUTER_SVC_ADDRESS_MAX_RETRIES", "5")
	os.Setenv("ROUTER_SVC_ADDRESS_UPDATE_TIMEOUT", "30s")
	os.Setenv("ROUTER_UNTAP_SERVICE_TIMEOUT", "3600s")
	os.Setenv("USE_ENCODED_PATH", "false")
	os.Setenv("DISPLAY_ACCESS_LOG", "false")
	os.Setenv("DEBUG_ENV", "false")
	routerPort, err := utils.FindFreePort()
	if err != nil {
		return fmt.Errorf("error finding unused port: %v", err)
	}
	err = f.ToggleMetricAddr()
	if err != nil {
		return fmt.Errorf("error toggling metric address: %v", err)
	}
	err = router.Start(ctx, f.ClientGen(), f.Logger(), routerPort, fmt.Sprintf("http://localhost:%d", executorPort))
	if err != nil {
		return fmt.Errorf("error starting router: %v", err)
	}
	f.ServiceInfo["router"] = framework.ServiceInfo{
		Port: routerPort,
	}
	return nil
}