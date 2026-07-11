// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"os"
	"strconv"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.

	"github.com/go-logr/logr"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"

	//+kubebuilder:scaffold:imports

	"github.com/fission/fission/pkg/svcinfo"

	"github.com/fission/fission/pkg/utils/httpserver"
)

type WebhookInjector interface {
	SetupWebhookWithManager(mgr manager.Manager) error
}

func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, options webhook.Options) (err error) {
	wLogger := logger.WithName("webhook")

	metricsAddr := httpserver.BindAddrFromEnv("METRICS_ADDR", svcinfo.PortMetrics)
	if ephemeral, _ := strconv.ParseBool(os.Getenv("FISSION_TEST_EPHEMERAL_SERVERS")); ephemeral {
		// In-process e2e harness: bind an ephemeral metrics port so the manager
		// can't lose a TOCTOU race for a fixed port against the other in-process
		// managers — matching executor/buildermgr/router (pkg/.../start.go).
		metricsAddr = ":0"
	}
	mgrOpt := manager.Options{
		Scheme: scheme.Scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		WebhookServer: webhook.NewServer(options),
		Logger:        wLogger,
	}
	restConfig, err := clientGen.GetRestConfig()
	if err != nil {
		wLogger.Error(err, "unable to get rest config")
		return err
	}
	// Setup a Manager
	mgr, err := manager.New(restConfig, mgrOpt)
	if err != nil {
		wLogger.Error(err, "unable to set up overall controller manager")
		return err
	}

	// Setup webhooks

	// HTTPTrigger, TimeTrigger and CanaryConfig no longer need a webhook: their
	// field rules are enforced by the API server via CEL, their parser-based
	// rules that CEL cannot express (cron schedule; CORS origin/max-age and
	// ingress path regex) are reported as status Conditions by the timer and
	// router reconcilers, and CanaryConfig had no validation.
	//
	// The remaining CRDs keep a webhook for the checks CEL cannot express —
	// cross-namespace references, pod-spec/container security (GHSA), the
	// environment runtime-image/name invariant, message-queue type/topic
	// validity, and reference-name (DNS-1123) checks — which still run via each
	// type's Validate(). CEL covers their field rules too; the overlap is
	// deliberate defense-in-depth.
	webhookInjectors := []WebhookInjector{
		&Environment{},
		&Package{},
		&Function{},
		&MessageQueueTrigger{},
		&KubernetesWatchTrigger{},
	}

	for _, injector := range webhookInjectors {
		if err := injector.SetupWebhookWithManager(mgr); err != nil {
			wLogger.Error(err, "unable to create webhook")
			return err
		}
	}

	wLogger.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		wLogger.Error(err, "unable to run manager")
		return err
	}
	return nil
}
