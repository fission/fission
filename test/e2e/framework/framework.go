package framework

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"go.uber.org/zap"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type ServiceInfo struct {
	Port int
}

type Framework struct {
	env         *envtest.Environment
	config      *rest.Config
	logger      *zap.Logger
	serviceInfo map[string]ServiceInfo
}

func NewWebhookOptions() (*envtest.WebhookInstallOptions, error) {
	webhookPort, err := utils.FindFreePort()
	if err != nil {
		return nil, fmt.Errorf("error finding unused port: %v", err)
	}
	_, filename, _, _ := runtime.Caller(0) //nolint
	root := filepath.Dir(filename)

	options := &envtest.WebhookInstallOptions{
		LocalServingHost: "localhost",
		LocalServingPort: webhookPort,
		Paths:            []string{filepath.Join(root, "webhook-manifest.yaml")},
	}
	err = options.PrepWithoutInstalling()
	if err != nil {
		return nil, fmt.Errorf("error preparing webhook install options: %v", err)
	}
	return options, nil
}

func NewFramework() *Framework {
	webhookOptions, err := NewWebhookOptions()
	if err != nil {
		panic(err)
	}
	_, filename, _, _ := runtime.Caller(0) //nolint
	root := filepath.Dir(filename)
	crdPath := filepath.Join(root, "..", "..", "..", "crds", "v1")

	return &Framework{
		logger: loggerfactory.GetLogger(),
		env: &envtest.Environment{
			CRDDirectoryPaths:     []string{crdPath},
			ErrorIfCRDPathMissing: true,
			CRDInstallOptions: envtest.CRDInstallOptions{
				MaxTime: 60 * time.Second,
			},
			WebhookInstallOptions: *webhookOptions,
			BinaryAssetsDirectory: os.Getenv("KUBEBUILDER_ASSETS"),
		},
		serviceInfo: make(map[string]ServiceInfo),
	}
}

func (f *Framework) GetEnv() *envtest.Environment {
	return f.env
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
	f.logger.Info("Stopping test env")
	err := f.logger.Sync()
	if err != nil {
		return fmt.Errorf("error syncing logger: %v", err)
	}
	err = f.env.Stop()
	if err != nil {
		return fmt.Errorf("error stopping test env: %v", err)
	}
	return nil
}

func (f *Framework) AddServiceInfo(name string, info ServiceInfo) {
	f.serviceInfo[name] = info
	f.logger.Info("Added service", zap.String("name", name), zap.Any("info", info))
}

func (f *Framework) GetServiceURL(name string) (string, error) {
	info, ok := f.serviceInfo[name]
	if !ok {
		return "", fmt.Errorf("service %s not found", name)
	}
	if info.Port == 0 {
		return "", fmt.Errorf("service %s port not set", name)
	}
	return fmt.Sprintf("http://localhost:%d", info.Port), nil
}

func (f *Framework) CheckService(name string) error {
	_, err := f.GetServiceURL(name)
	if err != nil {
		return err
	}
	config := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // config is used to connect to our own webhook port.
	}

	d := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", net.JoinHostPort("localhost", strconv.Itoa(f.serviceInfo[name].Port)), config)
	if err != nil {
		return fmt.Errorf("webhook server is not reachable: %w", err)
	}

	if err := conn.Close(); err != nil {
		return fmt.Errorf("webhook server is not reachable: closing connection: %w", err)
	}

	return nil
}
