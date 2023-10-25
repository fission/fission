package framework

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

const (
	EXECUTOR_URL   = "http://executor.fission"
	STORAGESVC_URL = "http://storagesvc.fission"
)

type ServiceInfo struct {
	Port int
}

type Framework struct {
	env         *envtest.Environment
	config      *rest.Config
	logger      *zap.Logger
	ServiceInfo map[string]ServiceInfo
}

func NewFramework() *Framework {
	return &Framework{
		logger: loggerfactory.GetLogger(),
		env: &envtest.Environment{
			CRDDirectoryPaths:     []string{filepath.Join("../../..", "crds", "v1")},
			ErrorIfCRDPathMissing: true,
			CRDInstallOptions: envtest.CRDInstallOptions{
				MaxTime: 60 * time.Second,
			},
			BinaryAssetsDirectory: os.Getenv("KUBEBUILDER_ASSETS"),
		},
		ServiceInfo: make(map[string]ServiceInfo),
	}
}

func (f *Framework) Start(ctx context.Context) error {
	var err error
	f.config, err = f.env.Start()
	if err != nil {
		return fmt.Errorf("error starting test env: %v", err)
	}
	return nil
}

func (f *Framework) ToggleMetricAddr() error {
	port, err := utils.FindFreePort()
	if err != nil {
		return fmt.Errorf("error finding unused port: %v", err)
	}
	os.Setenv("METRICS_ADDR", fmt.Sprint(port))
	return nil
}

func (f *Framework) RestConfig() *rest.Config {
	return f.config
}

func (f *Framework) Logger() *zap.Logger {
	return f.logger
}

func (f *Framework) ClientGen() *crd.ClientGenerator {
	return crd.NewClientGeneratorWithRestConfig(f.config)
}

func (f *Framework) Stop() error {
	err := f.env.Stop()
	if err != nil {
		return fmt.Errorf("error stopping test env: %v", err)
	}
	return nil
}
