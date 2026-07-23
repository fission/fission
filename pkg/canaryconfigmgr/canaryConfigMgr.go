// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfigmgr

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	"github.com/fission/fission/pkg/crd"
)

// failurePercentageGetter computes the error rate of the canary's new
// function/version over a time window. *PrometheusApiClient satisfies it in
// production; unit tests inject a deterministic fake. funcVersion is the
// alias-mode addition (RFC-0025 phase 5): empty means function-pair mode
// (byte-identical query to before the shim), non-empty adds a
// function_version label so two versions of the SAME function — which share
// function_name/function_namespace/path/method — are distinguishable series
// (see docs/rfc/0025-function-versions-aliases-rollback.md L181-182).
type failurePercentageGetter interface {
	GetFunctionFailurePercentage(ctx context.Context, path string, methods []string, funcName, funcVersion, funcNs, window string) (float64, error)
}

// specManagedAnnotation is the `fission spec` (GitOps) deployment-UID
// annotation key that marks a FunctionAlias as owned by a Git-tracked
// manifest. Duplicated here as a literal rather than importing
// pkg/fission-cli/cmd/spec.FISSION_DEPLOYMENT_UID_KEY: that package pulls in
// the CLI's cobra/cliwrapper dependency tree, which fission-bundle's canary
// server must not link. Mirrors the same guard in
// pkg/fission-cli/cmd/function/rollback.go.
const specManagedAnnotation = "fission-uid"

// setCanaryConfigConditions mirrors the bare Status string onto the standard
// Progressing/Ready conditions so `kubectl wait --for=condition=Ready
// canaryconfig/<name>` works alongside the legacy status. The mapping matches
// the enum in pkg/apis/core/v1/const.go. It reports whether any condition
// actually changed, so callers can skip a redundant status write.
// messageOverride, when non-empty, replaces the default per-status message —
// used for the alias-mode terminal-Failed paths (RFC-0025 plan-review
// blocker #5) where "traffic rolled back" is wrong: a reconcile-start
// validation refusal (missing alias, digest-pinned, spec-managed, ...) never
// touched the alias at all.
func setCanaryConfigConditions(s *fv1.CanaryConfigStatus, status string, gen int64, messageOverride string) bool {
	var (
		progStatus, readyStatus metav1.ConditionStatus
		reason, message         string
	)
	switch status {
	case fv1.CanaryConfigStatusPending:
		progStatus, readyStatus = metav1.ConditionTrue, metav1.ConditionFalse
		reason, message = fv1.CanaryConfigReasonInProgress, "canary rollout in progress"
	case fv1.CanaryConfigStatusSucceeded:
		progStatus, readyStatus = metav1.ConditionFalse, metav1.ConditionTrue
		reason, message = fv1.CanaryConfigReasonSucceeded, "canary rollout succeeded"
	case fv1.CanaryConfigStatusFailed:
		progStatus, readyStatus = metav1.ConditionFalse, metav1.ConditionFalse
		reason, message = fv1.CanaryConfigReasonFailed, "canary rollout failed; traffic rolled back"
	case fv1.CanaryConfigStatusAborted:
		progStatus, readyStatus = metav1.ConditionFalse, metav1.ConditionFalse
		reason, message = fv1.CanaryConfigReasonAborted, "canary rollout aborted"
	default:
		progStatus, readyStatus = metav1.ConditionUnknown, metav1.ConditionUnknown
		reason, message = fv1.CanaryConfigReasonUnknown, "unknown canary status: "+status
	}
	if messageOverride != "" {
		message = messageOverride
	}
	changed := conditions.Set(&s.Conditions, metav1.Condition{
		Type:               fv1.CanaryConfigConditionProgressing,
		Status:             progStatus,
		ObservedGeneration: gen,
		Reason:             reason,
		Message:            message,
	})
	if conditions.Set(&s.Conditions, metav1.Condition{
		Type:               fv1.CanaryConfigConditionReady,
		Status:             readyStatus,
		ObservedGeneration: gen,
		Reason:             reason,
		Message:            message,
	}) {
		changed = true
	}
	return changed
}

// canaryConfigMgr holds the side-effecting dependencies a single rollout step
// needs: the cache-backed client used to read the target HTTPTrigger and shift
// its function weights, and the Prometheus client used to read the new
// function's error rate. It is otherwise stateless — the controller-runtime
// workqueue plus RequeueAfter replace the per-config time.Ticker and
// cancel-func map the previous informer-based manager maintained.
type canaryConfigMgr struct {
	logger     logr.Logger
	client     client.Client
	apiReader  client.Reader
	promClient failurePercentageGetter
}

func MakeCanaryConfigMgr(logger logr.Logger, c client.Client, apiReader client.Reader, prometheusSvc string) (*canaryConfigMgr, error) {
	if prometheusSvc == "" {
		logger.Info("try to retrieve prometheus server information from environment variables")

		var prometheusSvcHost, prometheusSvcPort string
		// handle a case where there is a prometheus server is already installed, try to find the service from env variable
		for _, envVar := range os.Environ() {
			if strings.Contains(envVar, "PROMETHEUS_SERVER_SERVICE_HOST") {
				prometheusSvcHost = getEnvValue(envVar)
			} else if strings.Contains(envVar, "PROMETHEUS_SERVER_SERVICE_PORT") {
				prometheusSvcPort = getEnvValue(envVar)
			}
			if len(prometheusSvcHost) > 0 && len(prometheusSvcPort) > 0 {
				break
			}
		}
		if len(prometheusSvcHost) == 0 && len(prometheusSvcPort) == 0 {
			return nil, errors.New("unable to get prometheus service url")
		}
		prometheusSvc = fmt.Sprintf("http://%v:%v", prometheusSvcHost, prometheusSvcPort)
	}

	logger.Info("try to start canary config manager with prometheus service url", "prometheus", prometheusSvc)

	if _, err := url.Parse(prometheusSvc); err != nil {
		return nil, fmt.Errorf("prometheus service url not found/invalid, can't create canary config manager: %s", prometheusSvc)
	}

	promClient, err := MakePrometheusClient(logger, prometheusSvc)
	if err != nil {
		return nil, err
	}

	return &canaryConfigMgr{
		logger:     logger.WithName("canary_config_manager"),
		client:     c,
		apiReader:  apiReader,
		promClient: promClient,
	}, nil
}

// stepOutcome reports what a single reconcile step concluded.
type stepOutcome struct {
	// terminalStatus is non-empty (Succeeded or Failed) once the rollout is
	// finished, telling the reconciler to write that status and stop requeuing.
	terminalStatus string
	// requeue asks the reconciler to schedule another step after one
	// WeightIncrementDuration. Mutually exclusive with terminalStatus.
	requeue bool
	// message, when non-empty, overrides the terminalStatus's default
	// condition message (see setCanaryConfigConditions). Only ever set
	// alongside terminalStatus == Failed, for the alias-mode reconcile-start
	// validation refusals that never touched the alias.
	message string
}

// step advances the rollout by one weight increment, or rolls all traffic back
// and finishes if the new function's error rate crossed the threshold. It is
// the reconcile-friendly replacement for the old ticker-driven
// RollForwardOrBack: instead of stopping a ticker or closing a quit channel it
// returns a stepOutcome the reconciler maps onto a ctrl.Result and a status
// write.
func (m *canaryConfigMgr) step(ctx context.Context, cfg *fv1.CanaryConfig) (stepOutcome, error) {
	log := m.logger.WithValues("name", cfg.Name, "namespace", cfg.Namespace)

	trigger := &fv1.HTTPTrigger{}
	triggerKey := types.NamespacedName{Namespace: cfg.Namespace, Name: cfg.Spec.Trigger}
	if err := m.client.Get(ctx, triggerKey, trigger); err != nil {
		if apierrors.IsNotFound(err) {
			// The HTTPTrigger is gone — keep waiting for it to (re)appear rather
			// than failing the rollout. The old manager stopped here and relied
			// on its resync loop; a requeue is the direct equivalent.
			log.Error(err, "http trigger for canary config not found; will retry", "trigger", cfg.Spec.Trigger)
			return stepOutcome{requeue: true}, nil
		}
		return stepOutcome{}, err
	}

	// ALIAS MODE (RFC-0025 phase 5): an HTTPTrigger referencing a FunctionAlias
	// drives the rollout by stepping FunctionAlias.Weight instead of
	// HTTPTrigger.FunctionWeights. This branches BEFORE the function-weights
	// guard below — an alias-referencing trigger's FunctionWeights map is
	// irrelevant, not a misconfiguration.
	if trigger.Spec.FunctionReference.Type == fv1.FunctionReferenceTypeFunctionName && trigger.Spec.FunctionReference.Alias != "" {
		return m.stepAlias(ctx, cfg, trigger)
	}

	// A canary rollout only makes sense against a function-weights trigger with
	// a populated weights map; rollForward/rollbackWeights write into that map
	// and would panic on a nil one. A mismatched trigger is a (recoverable)
	// misconfiguration — keep requeuing so the rollout resumes if the trigger
	// is corrected, rather than panicking or giving up.
	if trigger.Spec.FunctionReference.Type != fv1.FunctionReferenceTypeFunctionWeights ||
		trigger.Spec.FunctionReference.FunctionWeights == nil {
		log.Info("http trigger is not configured for weighted canary; will retry",
			"trigger", trigger.Name, "type", trigger.Spec.FunctionReference.Type)
		return stepOutcome{requeue: true}, nil
	}

	// Only evaluate the failure rate once the new function is actually taking
	// traffic; at weight 0 there is nothing to observe.
	if trigger.Spec.FunctionReference.FunctionWeights[cfg.Spec.NewFunction] != 0 {
		urlPath, methods := triggerRouteInfo(trigger)

		// funcVersion is empty here: function-pair mode's NewFunction is
		// already a function name, so no function_version label is added —
		// the query is byte-identical to the pre-shim query.
		failurePercent, err := m.promClient.GetFunctionFailurePercentage(ctx, urlPath, methods,
			cfg.Spec.NewFunction, "", cfg.Namespace, cfg.Spec.WeightIncrementDuration)
		if err != nil {
			// Transient query error — check again next window rather than aborting.
			log.Error(err, "error calculating failure percentage; will retry")
			return stepOutcome{requeue: true}, nil
		}

		if failurePercent == -1 {
			// No requests reached the url in this window — nothing to evaluate.
			log.Info("no requests observed for url in window", "url", urlPath)
			return stepOutcome{requeue: true}, nil
		}

		if int(failurePercent) > cfg.Spec.FailureThreshold {
			log.Info("failure percentage crossed threshold; rolling back",
				"failure_percent", failurePercent, "threshold", cfg.Spec.FailureThreshold)
			if err := m.rollbackWeights(ctx, cfg, trigger); err != nil {
				// Never leave traffic on a failing function: surface the error so
				// the reconciler retries with backoff instead of finishing.
				log.Error(err, "error rolling back canary config")
				return stepOutcome{}, err
			}
			return stepOutcome{terminalStatus: fv1.CanaryConfigStatusFailed}, nil
		}
	}

	done, err := m.rollForward(ctx, cfg, trigger)
	if err != nil {
		log.Error(err, "error incrementing weights for trigger; will retry", "trigger", trigger.Name)
		return stepOutcome{requeue: true}, nil
	}
	if done {
		log.Info("canary rollout complete; new function now receives all traffic")
		return stepOutcome{terminalStatus: fv1.CanaryConfigStatusSucceeded}, nil
	}
	return stepOutcome{requeue: true}, nil
}

// triggerRouteInfo extracts the URL path and HTTP methods a canary rollout
// evaluates Prometheus traffic for, shared by function-pair and alias mode.
func triggerRouteInfo(trigger *fv1.HTTPTrigger) (path string, methods []string) {
	path = trigger.Spec.RelativeURL
	if trigger.Spec.Prefix != nil && *trigger.Spec.Prefix != "" {
		path = *trigger.Spec.Prefix
	}
	methods = trigger.Spec.Methods
	if len(trigger.Spec.Method) > 0 && !slices.Contains(trigger.Spec.Methods, trigger.Spec.Method) {
		methods = append(methods, trigger.Spec.Method)
	}
	return path, methods
}

// stepAlias is the alias-mode counterpart of step()'s function-pair body
// (RFC-0025 phase 5, docs/rfc/0025-function-versions-aliases-rollback.md
// "CanaryConfig absorption"). ROLE MAPPING per the RFC: cfg.Spec.OldFunction
// stays the alias's PRIMARY (Spec.Version) for the entire rollout;
// cfg.Spec.NewFunction is the SECONDARY (Spec.SecondaryVersion); Spec.Weight
// (the primary's share) steps DOWN from 100 by WeightIncrement each interval,
// so the secondary's share (100-Weight) grows. Spec.Version only ever changes
// on the single terminal SUCCESS write (rollForwardAlias's "done" path) —
// that is the shim's one write that can produce an AliasReconciler History
// append; every other write (progression steps, and the terminal FAILURE
// write) leaves Spec.Version at cfg.Spec.OldFunction and so appends nothing.
func (m *canaryConfigMgr) stepAlias(ctx context.Context, cfg *fv1.CanaryConfig, trigger *fv1.HTTPTrigger) (stepOutcome, error) {
	aliasName := trigger.Spec.FunctionReference.Alias
	log := m.logger.WithValues("name", cfg.Name, "namespace", cfg.Namespace, "alias", aliasName)

	alias, failReason, err := m.validateAliasRollout(ctx, cfg, aliasName)
	if err != nil {
		return stepOutcome{}, err
	}
	if failReason != "" {
		// Reconcile-start validation refused the rollout — the alias is never
		// touched, so there is nothing to roll back.
		log.Info("alias-mode canary validation failed; failing rollout without touching the alias", "reason", failReason)
		return stepOutcome{terminalStatus: fv1.CanaryConfigStatusFailed, message: failReason}, nil
	}

	primaryWeight := 100
	if alias.Spec.Weight != nil {
		primaryWeight = *alias.Spec.Weight
	}

	// Only evaluate the failure rate once the secondary is actually taking
	// traffic; at primary weight 100 there is nothing to observe.
	if primaryWeight < 100 {
		urlPath, methods := triggerRouteInfo(trigger)

		// function_name is the alias's FUNCTION, not cfg.Spec.NewFunction (a
		// VERSION name) — both the primary and secondary targets are versions
		// of that one function and so share function_name/function_namespace/
		// path/method; function_version disambiguates which of the two a
		// given series belongs to (RFC L181-182). Passing NewFunction as
		// function_name would match zero series, wedging the rollout in a
		// permanent requeue (failurePercent == -1 forever).
		failurePercent, err := m.promClient.GetFunctionFailurePercentage(ctx, urlPath, methods,
			alias.Spec.FunctionName, cfg.Spec.NewFunction, cfg.Namespace, cfg.Spec.WeightIncrementDuration)
		if err != nil {
			log.Error(err, "error calculating failure percentage; will retry")
			return stepOutcome{requeue: true}, nil
		}

		if failurePercent == -1 {
			log.Info("no requests observed for url in window", "url", urlPath)
			return stepOutcome{requeue: true}, nil
		}

		if int(failurePercent) > cfg.Spec.FailureThreshold {
			log.Info("failure percentage crossed threshold; rolling back alias",
				"failure_percent", failurePercent, "threshold", cfg.Spec.FailureThreshold)
			if err := m.rollbackAlias(ctx, cfg, alias); err != nil {
				log.Error(err, "error rolling back alias canary")
				return stepOutcome{}, err
			}
			return stepOutcome{terminalStatus: fv1.CanaryConfigStatusFailed}, nil
		}
	}

	done, err := m.rollForwardAlias(ctx, cfg, alias)
	if err != nil {
		log.Error(err, "error stepping alias weight; will retry", "alias", aliasName)
		return stepOutcome{requeue: true}, nil
	}
	if done {
		log.Info("canary rollout complete; alias now fully resolves to the new version")
		return stepOutcome{terminalStatus: fv1.CanaryConfigStatusSucceeded}, nil
	}
	return stepOutcome{requeue: true}, nil
}

// validateAliasRollout performs the reconcile-start checks an alias-mode
// canary must pass before step() may touch anything (RFC-0025 plan-review
// blocker #5): the FunctionAlias exists; cfg.Spec.NewFunction/OldFunction
// each name a FunctionVersion belonging to the alias's function; and the
// alias is neither digest-pinned nor spec-managed — either of which the
// shim's writes would silently corrupt (a digest-pinned alias's success
// write would need to clear PackageDigest, converting a GitOps content pin
// into a name pin behind the pipeline's back; a spec-managed alias would
// have its promotion reverted by the very next `spec apply`, per the RFC's
// Git-ownership rule — see pkg/fission-cli/cmd/function/rollback.go's
// identical guard). A non-empty failReason means the caller must terminate
// the rollout Failed WITHOUT writing the alias.
//
// Reads go through m.apiReader (uncached), matching updateHttpTriggerWithRetries's
// rationale: an uncached read observes a concurrent edit to the alias or its
// target versions on the very next step rather than serving a stale
// informer-cached copy.
func (m *canaryConfigMgr) validateAliasRollout(ctx context.Context, cfg *fv1.CanaryConfig, aliasName string) (*fv1.FunctionAlias, string, error) {
	alias := &fv1.FunctionAlias{}
	aliasKey := types.NamespacedName{Namespace: cfg.Namespace, Name: aliasName}
	if err := m.apiReader.Get(ctx, aliasKey, alias); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Sprintf("function alias %s not found", aliasKey), nil
		}
		return nil, "", err
	}

	if alias.Spec.PackageDigest != "" {
		return nil, fmt.Sprintf("function alias %s is digest-pinned (packageDigest %q); alias-mode canary requires a name-pinned alias (spec.version), not a declarative digest pin",
			aliasKey, alias.Spec.PackageDigest), nil
	}
	if uid, managed := alias.Annotations[specManagedAnnotation]; managed && uid != "" {
		return nil, fmt.Sprintf("function alias %s is managed by `fission spec` (Git); the promotion write would be reverted by the next `spec apply` — detach it from the deployment (see `fission fn rollback --detach`) before running an alias-mode canary against it",
			aliasKey), nil
	}

	for _, versionName := range []string{cfg.Spec.NewFunction, cfg.Spec.OldFunction} {
		v := &fv1.FunctionVersion{}
		if err := m.apiReader.Get(ctx, types.NamespacedName{Namespace: cfg.Namespace, Name: versionName}, v); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Sprintf("function version %s/%s not found", cfg.Namespace, versionName), nil
			}
			return nil, "", err
		}
		if v.Spec.FunctionName != alias.Spec.FunctionName {
			return nil, fmt.Sprintf("function version %s/%s belongs to function %q, not alias %s's function %q",
				cfg.Namespace, versionName, v.Spec.FunctionName, aliasKey, alias.Spec.FunctionName), nil
		}
	}

	return alias, "", nil
}

// rollForwardAlias steps WeightIncrement percent of the primary's share onto
// the alias's secondary target (cfg.Spec.NewFunction), clamping at 0. It
// reports whether the primary has reached weight 0 (the rollout is done).
// Per stepAlias's role mapping, Spec.Version is written as
// cfg.Spec.OldFunction on every progression step — the same value it already
// holds, so this never produces an AliasReconciler History append — and is
// repointed to cfg.Spec.NewFunction ONLY on the terminal "done" write, the
// shim's single History-producing write per rollout.
func (m *canaryConfigMgr) rollForwardAlias(ctx context.Context, cfg *fv1.CanaryConfig, alias *fv1.FunctionAlias) (bool, error) {
	primaryWeight := 100
	if alias.Spec.Weight != nil {
		primaryWeight = *alias.Spec.Weight
	}

	if primaryWeight-cfg.Spec.WeightIncrement <= 0 {
		m.logger.Info("alias canary rollout complete; promoting secondary to primary",
			"name", cfg.Name, "namespace", cfg.Namespace, "alias", alias.Name, "version", cfg.Spec.NewFunction)
		return true, m.updateFunctionAliasWithRetries(ctx, alias.Namespace, alias.Name, cfg.Spec.NewFunction, nil, "")
	}

	newWeight := primaryWeight - cfg.Spec.WeightIncrement
	m.logger.Info("stepped down alias primary weight",
		"name", cfg.Name, "namespace", cfg.Namespace, "alias", alias.Name, "primary_weight", newWeight)
	return false, m.updateFunctionAliasWithRetries(ctx, alias.Namespace, alias.Name, cfg.Spec.OldFunction, &newWeight, cfg.Spec.NewFunction)
}

// rollbackAlias repoints the alias fully back to the primary (old) target,
// clearing Weight and SecondaryVersion. Spec.Version is written as
// cfg.Spec.OldFunction — its value for the whole rollout — so this write
// never changes ResolvedVersion and produces ZERO History appends; contrast
// with rollForwardAlias's terminal "done" write, which does repoint Version
// and is the only write that appends.
func (m *canaryConfigMgr) rollbackAlias(ctx context.Context, cfg *fv1.CanaryConfig, alias *fv1.FunctionAlias) error {
	return m.updateFunctionAliasWithRetries(ctx, alias.Namespace, alias.Name, cfg.Spec.OldFunction, nil, "")
}

// updateFunctionAliasWithRetries persists version/weight/secondaryVersion
// onto the FunctionAlias named by namespace/name, retrying the
// optimistic-concurrency conflicts a concurrent write produces — mirroring
// updateHttpTriggerWithRetries. The re-read goes through the uncached
// apiReader for the same reason: the cache-backed client would keep
// re-serving the stale object on every retry.
func (m *canaryConfigMgr) updateFunctionAliasWithRetries(ctx context.Context, namespace, name, version string, weight *int, secondaryVersion string) error {
	key := types.NamespacedName{Namespace: namespace, Name: name}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		alias := &fv1.FunctionAlias{}
		if err := m.apiReader.Get(ctx, key, alias); err != nil {
			return err
		}
		alias.Spec.Version = version
		alias.Spec.Weight = weight
		alias.Spec.SecondaryVersion = secondaryVersion
		return m.client.Update(ctx, alias)
	})
	if err != nil {
		return fmt.Errorf("error updating function alias %s: %w", key, err)
	}
	m.logger.V(1).Info("updated function alias rollout state", "alias", key)
	return nil
}

// rollForward shifts WeightIncrement percent of traffic from the old function
// to the new one, clamping at 100/0. It reports whether the new function has
// reached 100% (the rollout is done).
func (m *canaryConfigMgr) rollForward(ctx context.Context, cfg *fv1.CanaryConfig, trigger *fv1.HTTPTrigger) (bool, error) {
	weights := trigger.Spec.FunctionReference.FunctionWeights
	done := false
	if weights[cfg.Spec.NewFunction]+cfg.Spec.WeightIncrement >= 100 {
		done = true
		weights[cfg.Spec.NewFunction] = 100
		weights[cfg.Spec.OldFunction] = 0
	} else {
		weights[cfg.Spec.NewFunction] += cfg.Spec.WeightIncrement
		weights[cfg.Spec.OldFunction] = max(0, weights[cfg.Spec.OldFunction]-cfg.Spec.WeightIncrement)
	}

	m.logger.Info("incremented function weights",
		"name", cfg.Name, "namespace", cfg.Namespace, "function_weights", weights)

	return done, m.updateHttpTriggerWithRetries(ctx, trigger.Namespace, trigger.Name, weights)
}

// rollbackWeights shifts all traffic back to the old function. Unlike the old
// rollback it does not write the canary status — the reconciler owns the
// terminal Failed status write (writeStatus).
func (m *canaryConfigMgr) rollbackWeights(ctx context.Context, cfg *fv1.CanaryConfig, trigger *fv1.HTTPTrigger) error {
	weights := trigger.Spec.FunctionReference.FunctionWeights
	weights[cfg.Spec.NewFunction] = 0
	weights[cfg.Spec.OldFunction] = 100
	return m.updateHttpTriggerWithRetries(ctx, trigger.Namespace, trigger.Name, weights)
}

// updateHttpTriggerWithRetries persists fnWeights onto the HTTPTrigger,
// retrying the optimistic-concurrency conflicts that happen when a concurrent
// write bumps the trigger's ResourceVersion. The re-read goes through the
// uncached apiReader: the cache-backed client would re-serve the same stale
// object on each retry, so the conflict would never clear. A conflict that
// outlasts the backoff is returned so the reconciler can requeue.
func (m *canaryConfigMgr) updateHttpTriggerWithRetries(ctx context.Context, namespace, name string, fnWeights map[string]int) error {
	key := types.NamespacedName{Namespace: namespace, Name: name}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		trigger := &fv1.HTTPTrigger{}
		if err := m.apiReader.Get(ctx, key, trigger); err != nil {
			return err
		}
		// Copy the computed weights into the freshly-read trigger's own map
		// rather than assigning fnWeights wholesale: this keeps the two trigger
		// objects from sharing one map and preserves any unrelated weight
		// entries on the live object.
		if trigger.Spec.FunctionReference.FunctionWeights == nil {
			trigger.Spec.FunctionReference.FunctionWeights = map[string]int{}
		}
		maps.Copy(trigger.Spec.FunctionReference.FunctionWeights, fnWeights)
		return m.client.Update(ctx, trigger)
	})
	if err != nil {
		return fmt.Errorf("error updating http trigger %s: %w", key, err)
	}
	m.logger.V(1).Info("updated http trigger weights", "trigger", key)
	return nil
}

func getEnvValue(envVar string) string {
	_, value, _ := strings.Cut(envVar, "=")
	return value
}

// StartCanaryServer keeps the *errgroup.Group parameter for signature parity
// with the other fission-bundle subsystem entry points (the dispatcher in
// cmd/fission-bundle threads the same group into each); the canary controller
// runs on a controller-runtime Manager and does not use it.
func StartCanaryServer(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, _ *errgroup.Group, unitTestFlag bool) error {
	cLogger := logger.WithName("CanaryServer")

	restConfig, err := clientGen.GetRestConfig()
	if err != nil {
		return fmt.Errorf("failed to get rest config: %w", err)
	}

	err = ConfigureFeatures(ctx, restConfig, cLogger, unitTestFlag)
	if err != nil {
		cLogger.Error(err, "error configuring features - proceeding without optional features")
	}
	return err
}
