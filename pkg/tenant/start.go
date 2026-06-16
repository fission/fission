// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/utils"
)

// Start runs the multi-namespace tenant-lifecycle controller. It builds a
// leader-elected Manager with a cluster-wide cache (FissionTenant and Namespace
// are both cluster-scoped, low-sensitivity types — this is the design's single
// cluster-wide read, no core/workload type) and registers the tenant + namespace
// reconcilers. It serves no traffic; it reconciles CRs into the shared resolver
// set and reports readiness.
func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, _ *errgroup.Group) error {
	logger = logger.WithName("tenant-controller")

	restConfig, err := clientGen.GetRestConfig()
	if err != nil {
		return fmt.Errorf("failed to get rest config: %w", err)
	}
	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("failed to get fission client: %w", err)
	}
	// Gate on the Fission CRDs being served (FissionTenant ships alongside them),
	// so the Manager's informers don't error on a not-yet-registered type.
	if err := crd.WaitForFunctionCRDs(ctx, logger, fissionClient); err != nil {
		return fmt.Errorf("error waiting for CRDs: %w", err)
	}

	// The Manager needs both the k8s core scheme (Namespace) and the Fission
	// scheme (FissionTenant); the generated scheme.Scheme carries only the
	// Fission CRD types.
	tenantScheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(tenantScheme))
	utilruntime.Must(scheme.AddToScheme(tenantScheme))

	leaderElection, _ := strconv.ParseBool(os.Getenv("LEADER_ELECTION_ENABLED"))
	crMgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                        tenantScheme,
		Metrics:                       metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress:        "0",
		LeaderElection:                leaderElection,
		LeaderElectionID:              "fission-tenant-controller",
		LeaderElectionReleaseOnCancel: true,
		Logger:                        logger,
	})
	if err != nil {
		return fmt.Errorf("unable to set up tenant controller manager: %w", err)
	}

	resolver := utils.DefaultNSResolver()
	// The internal-auth master (empty when internalAuth is disabled) lets the
	// controller derive and provision per-namespace auth keys. Read here, not in
	// a library constructor, per the deterministic-constructor convention.
	master := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET"))
	tenantR := &TenantReconciler{logger: logger.WithName("tenant"), client: crMgr.GetClient(), resolver: resolver, master: master}
	// Watch FissionTenant spec changes AND Namespace create/delete: the Ready
	// condition and the resolver entry depend on whether the target namespace
	// exists, which the FissionTenant watch alone cannot observe.
	if err := builder.ControllerManagedBy(crMgr).
		For(&fv1.FissionTenant{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(tenantR.namespaceToRequests),
			builder.WithPredicates(namespaceExistencePredicate())).
		Named("fission-tenant").
		Complete(tenantR); err != nil {
		return fmt.Errorf("error registering fission-tenant reconciler: %w", err)
	}
	// The namespace reconciler triggers on the fission.io/enabled label rather
	// than a generation change, so it overrides the default predicate.
	if err := controller.RegisterWithPredicates(crMgr, &corev1.Namespace{},
		&NamespaceReconciler{logger: logger.WithName("namespace"), client: crMgr.GetClient()},
		"fission-tenant-namespace", 0, enabledLabelPredicate()); err != nil {
		return fmt.Errorf("error registering tenant namespace reconciler: %w", err)
	}

	logger.Info("starting tenant controller", "leaderElection", leaderElection)
	return crMgr.Start(ctx)
}

// enabledLabelPredicate admits only Namespaces currently carrying
// fission.io/enabled=true, so the namespace reconciler reconciles a namespace
// exactly when it is (or becomes) opted in.
func enabledLabelPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetLabels()[EnabledLabel] == "true"
	})
}

// namespaceExistencePredicate admits only Namespace create and delete events —
// the transitions that change whether a tenant's namespace exists. Label/spec
// updates are the namespace reconciler's concern and don't affect tenant
// readiness, so they're dropped to avoid needless tenant re-list churn.
func namespaceExistencePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}
