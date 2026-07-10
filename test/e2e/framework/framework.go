// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package framework

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/go-logr/logr"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type Framework struct {
	env    *envtest.Environment
	config *rest.Config
	logger logr.Logger
	// reg maps service names to their local TCP addresses; dialing a
	// registered name blocks until the backend accepts, so readiness
	// waits happen inside the dial instead of in caller poll loops.
	reg *portless.Registry
	// ports remembers each service's local port so GetServiceURL can hand
	// real dialable URLs to production consumers (in-process CLI,
	// publishers) that read plain URLs from env vars.
	ports map[string]int
}

func NewWebhookOptions() (*envtest.WebhookInstallOptions, error) {
	webhookPort, err := utils.FindFreePort()
	if err != nil {
		return nil, fmt.Errorf("error finding unused port: %w", err)
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
		return nil, fmt.Errorf("error preparing webhook install options: %w", err)
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
		reg:   portless.New(),
		ports: make(map[string]int),
	}
}

func (f *Framework) GetEnv() *envtest.Environment {
	return f.env
}

func (f *Framework) Start(ctx context.Context) error {
	var err error
	f.config, err = f.env.Start()
	if err != nil {
		return fmt.Errorf("error starting test env: %w", err)
	}
	return nil
}

func (f *Framework) ToggleMetricAddr() error {
	port, err := utils.FindFreePort()
	if err != nil {
		return fmt.Errorf("error finding unused port: %w", err)
	}
	os.Setenv("METRICS_ADDR", fmt.Sprint(port))
	return nil
}

func (f *Framework) RestConfig() *rest.Config {
	return f.config
}

func (f *Framework) Logger() logr.Logger {
	return f.logger
}

func (f *Framework) ClientGen() *crd.ClientGenerator {
	return crd.NewClientGeneratorWithRestConfig(f.config)
}

func (f *Framework) Stop() error {
	f.logger.Info("Stopping test env")
	if err := f.reg.Close(); err != nil {
		f.logger.Error(err, "error closing portless registry")
	}
	err := f.env.Stop()
	if err != nil {
		return fmt.Errorf("error stopping test env: %w", err)
	}
	return nil
}

// RegisterService names a locally-bound service in the portless registry.
// The service may still be starting: dials through the registry (WaitReady)
// retry until the port accepts (and any route health check passes).
func (f *Framework) RegisterService(name string, port int, opts ...portless.RouteOption) error {
	if _, err := f.reg.Add(context.Background(), name, backend.TCP(fmt.Sprintf("127.0.0.1:%d", port)), opts...); err != nil {
		return fmt.Errorf("error registering service %s: %w", name, err)
	}
	f.ports[name] = port
	f.logger.Info("Registered service", "name", name, "port", port)
	return nil
}

// WithTLSReadyCheck gates a route's readiness on a completed TLS handshake,
// not just a TCP accept — use it for TLS servers (the webhook) where
// "listening" and "able to serve TLS" can differ (e.g. bad cert material).
func WithTLSReadyCheck() portless.RouteOption {
	return portless.RouteWithHealthCheck(func(ctx context.Context, dial portless.DialFunc) error {
		conn, err := dial(ctx, "tcp", "health:0")
		if err != nil {
			return err
		}
		defer conn.Close()
		tlsConn := tls.Client(conn, &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // readiness probe against our own webhook port.
		})
		defer tlsConn.Close()
		return tlsConn.HandshakeContext(ctx)
	})
}

// GetServiceURL returns the real local URL of a registered service. Production
// consumers (the in-process CLI, publishers) dial these URLs with plain HTTP
// clients, so they must stay resolvable outside the registry.
func (f *Framework) GetServiceURL(name string) (string, error) {
	port, ok := f.ports[name]
	if !ok {
		return "", fmt.Errorf("service %s not found", name)
	}
	return fmt.Sprintf("http://localhost:%d", port), nil
}

// WaitReady blocks until the named service accepts TCP connections and its
// route health check (if any — see WithTLSReadyCheck) passes, or ctx expires.
// Replaces caller-side poll loops around the old CheckService.
func (f *Framework) WaitReady(ctx context.Context, name string) error {
	rt, ok := f.reg.Lookup(name)
	if !ok {
		return fmt.Errorf("service %s not found", name)
	}
	return rt.Ready(ctx)
}
