package test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/fission/fission/cmd/fission-cli/app"
	"github.com/fission/fission/pkg/buildermgr"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/router"
	"github.com/fission/fission/pkg/storagesvc"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

const (
	EXECUTOR_URL   = "http://executor.fission"
	STORAGESVC_URL = "http://storagesvc.fission"
)

type Framework struct {
	env            *envtest.Environment
	config         *rest.Config
	logger         *zap.Logger
	CancelFunc     context.CancelFunc
	executorPort   int
	storageSvcPort int
	routerPort     int
}

func NewFramework() *Framework {
	return &Framework{
		logger: loggerfactory.GetLogger(),
		env: &envtest.Environment{
			CRDDirectoryPaths:     []string{filepath.Join("..", "crds", "v1")},
			ErrorIfCRDPathMissing: true,
			CRDInstallOptions: envtest.CRDInstallOptions{
				MaxTime: 60 * time.Second,
			},
			BinaryAssetsDirectory: os.Getenv("KUBEBUILDER_ASSETS"),
		},
	}
}

func (f *Framework) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	f.CancelFunc = cancel
	var err error
	f.config, err = f.env.Start()
	if err != nil {
		return fmt.Errorf("error starting test env: %v", err)
	}
	clientGen := f.ClientGen()

	f.executorPort, err = utils.FindFreePort()
	if err != nil {
		return fmt.Errorf("error finding unused port: %v", err)
	}
	err = f.toggleMetricAddr()
	if err != nil {
		return fmt.Errorf("error toggling metric address: %v", err)
	}
	err = executor.StartExecutor(ctx, clientGen, f.logger, f.executorPort)
	if err != nil {
		return fmt.Errorf("error starting executor: %v", err)
	}

	os.Setenv("PRUNE_ENABLED", "true")
	os.Setenv("PRUNE_INTERVAL", "60")
	storageDir, err := os.MkdirTemp("/tmp", "storagesvc")
	if err != nil {
		return fmt.Errorf("error creating temp directory: %v", err)
	}

	f.storageSvcPort, err = utils.FindFreePort()
	if err != nil {
		return fmt.Errorf("error finding unused port: %v", err)
	}
	err = f.toggleMetricAddr()
	if err != nil {
		return fmt.Errorf("error toggling metric address: %v", err)
	}
	err = storagesvc.Start(ctx, f.logger, storagesvc.NewLocalStorage(storageDir), f.storageSvcPort)
	if err != nil {
		return fmt.Errorf("error starting storage service: %v", err)
	}
	err = f.toggleMetricAddr()
	if err != nil {
		return fmt.Errorf("error toggling metric address: %v", err)
	}
	err = buildermgr.Start(ctx, clientGen, f.logger, fmt.Sprintf("http://localhost:%d", f.storageSvcPort))
	if err != nil {
		return fmt.Errorf("error starting builder manager: %v", err)
	}

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
	f.routerPort, err = utils.FindFreePort()
	if err != nil {
		return fmt.Errorf("error finding unused port: %v", err)
	}
	err = f.toggleMetricAddr()
	if err != nil {
		return fmt.Errorf("error toggling metric address: %v", err)
	}
	err = router.Start(ctx, clientGen, f.logger, f.routerPort, fmt.Sprintf("http://localhost:%d", f.executorPort))
	if err != nil {
		return fmt.Errorf("error starting router: %v", err)
	}

	return nil
}

func (f *Framework) toggleMetricAddr() error {
	port, err := utils.FindFreePort()
	if err != nil {
		return fmt.Errorf("error finding unused port: %v", err)
	}
	os.Setenv("METRICS_ADDR", fmt.Sprint(port))
	return nil
}

func (f *Framework) Config() *rest.Config {
	return f.config
}

func (f *Framework) Logger() *zap.Logger {
	return f.logger
}

func (f *Framework) ClientGen() *crd.ClientGenerator {
	return crd.NewClientGeneratorWithRestConfig(f.config)
}

func (f *Framework) Stop() error {
	f.CancelFunc()
	err := f.env.Stop()
	if err != nil {
		return fmt.Errorf("error stopping test env: %v", err)
	}
	return nil
}

func (f *Framework) ExecCommand(ctx context.Context, args ...string) (string, error) {
	cmd := app.App(cmd.ClientOptions{
		RestConfig: f.config,
	})
	cmd.SilenceErrors = true // use our own error message printer
	cmd.SetArgs(args)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := cmd.ExecuteContext(ctx)
	return buf.String(), err
}
