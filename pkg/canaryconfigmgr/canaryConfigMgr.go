// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfigmgr

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	"github.com/fission/fission/pkg/crd"
)

const maxRetries = 10

// failurePercentageGetter computes the error rate of the canary's new function
// over a time window. *PrometheusApiClient satisfies it in production; unit
// tests inject a deterministic fake.
type failurePercentageGetter interface {
	GetFunctionFailurePercentage(ctx context.Context, path string, methods []string, funcName, funcNs, window string) (float64, error)
}

// setCanaryConfigConditions mirrors the bare Status string onto the standard
// Progressing/Ready conditions so `kubectl wait --for=condition=Ready
// canaryconfig/<name>` works alongside the legacy status. The mapping matches
// the enum in pkg/apis/core/v1/const.go. It reports whether any condition
// actually changed, so callers can skip a redundant status write.
func setCanaryConfigConditions(s *fv1.CanaryConfigStatus, status string, gen int64) bool {
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
	promClient failurePercentageGetter
}

func MakeCanaryConfigMgr(logger logr.Logger, c client.Client, prometheusSvc string) (*canaryConfigMgr, error) {
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
		urlPath := trigger.Spec.RelativeURL
		if trigger.Spec.Prefix != nil && *trigger.Spec.Prefix != "" {
			urlPath = *trigger.Spec.Prefix
		}
		methods := trigger.Spec.Methods
		if len(trigger.Spec.Method) > 0 && !slices.Contains(trigger.Spec.Methods, trigger.Spec.Method) {
			methods = append(methods, trigger.Spec.Method)
		}

		failurePercent, err := m.promClient.GetFunctionFailurePercentage(ctx, urlPath, methods,
			cfg.Spec.NewFunction, cfg.Namespace, cfg.Spec.WeightIncrementDuration)
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

// updateHttpTriggerWithRetries persists fnWeights onto the HTTPTrigger. The
// short retry loop absorbs the brief optimistic-concurrency conflicts that
// happen when the cached trigger lags a concurrent write; a conflict that
// outlasts the loop is returned so the reconciler can requeue and reconverge
// once the watch refreshes the cache.
func (m *canaryConfigMgr) updateHttpTriggerWithRetries(ctx context.Context, namespace, name string, fnWeights map[string]int) error {
	key := types.NamespacedName{Namespace: namespace, Name: name}
	var lastErr error
	for range maxRetries {
		trigger := &fv1.HTTPTrigger{}
		if err := m.client.Get(ctx, key, trigger); err != nil {
			return fmt.Errorf("error getting http trigger %s: %w", key, err)
		}
		trigger.Spec.FunctionReference.FunctionWeights = fnWeights

		switch err := m.client.Update(ctx, trigger); {
		case err == nil:
			m.logger.V(1).Info("updated http trigger weights", "trigger", key)
			return nil
		case apierrors.IsConflict(err):
			lastErr = err
			continue
		default:
			return fmt.Errorf("error updating http trigger %s: %w", key, err)
		}
	}
	return fmt.Errorf("error updating http trigger %s after %d retries: %w", key, maxRetries, lastErr)
}

func getEnvValue(envVar string) string {
	_, value, _ := strings.Cut(envVar, "=")
	return value
}

func StartCanaryServer(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr *errgroup.Group, unitTestFlag bool) error {
	cLogger := logger.WithName("CanaryServer")

	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("failed to get fission client: %w", err)
	}
	restConfig, err := clientGen.GetRestConfig()
	if err != nil {
		return fmt.Errorf("failed to get rest config: %w", err)
	}

	err = ConfigureFeatures(ctx, restConfig, cLogger, unitTestFlag, fissionClient, mgr)
	if err != nil {
		cLogger.Error(err, "error configuring features - proceeding without optional features")
	}
	return err
}
