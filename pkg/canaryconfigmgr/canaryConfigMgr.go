/*
Copyright 2016 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package canaryconfigmgr

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/manager"
)

const (
	maxRetries = 10
)

type canaryConfigMgr struct {
	logger                 logr.Logger
	fissionClient          versioned.Interface
	kubeClient             kubernetes.Interface
	canaryConfigInformer   map[string]k8sCache.SharedIndexInformer
	promClient             *PrometheusApiClient
	canaryCfgCancelFuncMap *canaryConfigCancelFuncMap
}

func MakeCanaryConfigMgr(ctx context.Context, logger logr.Logger, fissionClient versioned.Interface, kubeClient kubernetes.Interface, prometheusSvc string) (*canaryConfigMgr, error) {
	if prometheusSvc == "" {
		logger.Info("try to retrieve prometheus server information from environment variables")

		var prometheusSvcHost, prometheusSvcPort string
		// handle a case where there is a prometheus server is already installed, try to find the service from env variable
		envVars := os.Environ()

		for _, envVar := range envVars {
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

	_, err := url.Parse(prometheusSvc)
	if err != nil {
		return nil, fmt.Errorf("prometheus service url not found/invalid, can't create canary config manager: %s", prometheusSvc)
	}

	promClient, err := MakePrometheusClient(logger, prometheusSvc)
	if err != nil {
		return nil, err
	}

	configMgr := &canaryConfigMgr{
		logger:                 logger.WithName("canary_config_manager"),
		fissionClient:          fissionClient,
		kubeClient:             kubeClient,
		promClient:             promClient,
		canaryCfgCancelFuncMap: makecanaryConfigCancelFuncMap(),
	}
	configMgr.canaryConfigInformer = utils.GetInformersForNamespaces(fissionClient, time.Minute*30, fv1.CanaryConfigResource)
	err = configMgr.CanaryConfigEventHandlers(ctx)
	if err != nil {
		return nil, err
	}
	return configMgr, nil
}

func (canaryCfgMgr *canaryConfigMgr) CanaryConfigEventHandlers(ctx context.Context) error {
	for _, informer := range canaryCfgMgr.canaryConfigInformer {
		_, err := informer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				canaryConfig := obj.(*fv1.CanaryConfig)
				if canaryConfig.Status.Status == fv1.CanaryConfigStatusPending {
					go canaryCfgMgr.addCanaryConfig(ctx, canaryConfig)
				}
			},
			DeleteFunc: func(obj any) {
				canaryConfig := obj.(*fv1.CanaryConfig)
				go canaryCfgMgr.deleteCanaryConfig(canaryConfig)
			},
			UpdateFunc: func(oldObj any, newObj any) {
				oldConfig := oldObj.(*fv1.CanaryConfig)
				newConfig := newObj.(*fv1.CanaryConfig)
				if oldConfig.ResourceVersion != newConfig.ResourceVersion &&
					newConfig.Status.Status == fv1.CanaryConfigStatusPending {
					canaryCfgMgr.logger.Info("update canary config invoked",
						"name", newConfig.Name,
						"namespace", newConfig.Namespace,
						"version", newConfig.ResourceVersion)
					go canaryCfgMgr.updateCanaryConfig(ctx, oldConfig, newConfig)
				}
				go canaryCfgMgr.reSyncCanaryConfigs(ctx)

			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (canaryCfgMgr *canaryConfigMgr) Run(ctx context.Context, mgr manager.Interface) {
	mgr.AddInformers(ctx, canaryCfgMgr.canaryConfigInformer)
	canaryCfgMgr.logger.Info("started canary configmgr controller")
}

func (canaryCfgMgr *canaryConfigMgr) addCanaryConfig(ctx context.Context, canaryConfig *fv1.CanaryConfig) {
	canaryCfgMgr.logger.V(1).Info("addCanaryConfig called", "canary_config", canaryConfig.Name)

	// for each canary config, create a ticker with increment interval
	interval, err := time.ParseDuration(canaryConfig.Spec.WeightIncrementDuration)
	if err != nil {
		canaryCfgMgr.logger.Error(err, "error parsing duration - can't proceed with this canaryConfig", "duration", canaryConfig.Spec.WeightIncrementDuration,
			"name", canaryConfig.Name,
			"namespace", canaryConfig.Namespace,
			"version", canaryConfig.ResourceVersion)
		return
	}
	ticker := time.NewTicker(interval)

	// create a context cancel func for each canary config. this will be used to cancel the processing of this canary
	// config in the event that it's deleted
	ctx, cancel := context.WithCancel(ctx)

	cacheValue := &CanaryProcessingInfo{
		CancelFunc: &cancel,
		Ticker:     ticker,
	}
	err = canaryCfgMgr.canaryCfgCancelFuncMap.assign(&canaryConfig.ObjectMeta, cacheValue)
	if err != nil {
		canaryCfgMgr.logger.Error(err, "error caching canary config", "name", canaryConfig.Name,
			"namespace", canaryConfig.Namespace,
			"version", canaryConfig.ResourceVersion)
		return
	}
	canaryCfgMgr.processCanaryConfig(&ctx, canaryConfig, ticker)
}

func (canaryCfgMgr *canaryConfigMgr) processCanaryConfig(ctx *context.Context, canaryConfig *fv1.CanaryConfig, ticker *time.Ticker) {
	quit := make(chan struct{})

	for {
		select {
		case <-(*ctx).Done():
			// this case when someone deleted their canary config in the middle of it being processed
			canaryCfgMgr.logger.Info("cancel func called for canary config",
				"name", canaryConfig.Name,
				"namespace", canaryConfig.Namespace,
				"version", canaryConfig.ResourceVersion)
			canaryCfgMgr.canaryCfgCancelFuncMap.remove(&canaryConfig.ObjectMeta)
			return

		case <-ticker.C:
			// every weightIncrementDuration, check if failureThreshold has reached.
			// if yes, rollback.
			// else, increment the weight of new function and decrement old function by `weightIncrement`
			canaryCfgMgr.logger.Info("processing canary config",
				"name", canaryConfig.Name,
				"namespace", canaryConfig.Namespace,
				"version", canaryConfig.ResourceVersion)
			canaryCfgMgr.RollForwardOrBack(*ctx, canaryConfig, quit, ticker)

		case <-quit:
			// we're done processing this canary config either because the new function receives 100% of the traffic
			// or we rolled back to send all 100% traffic to old function
			canaryCfgMgr.logger.Info("quit processing canaryConfig",
				"name", canaryConfig.Name,
				"namespace", canaryConfig.Namespace,
				"version", canaryConfig.ResourceVersion)
			canaryCfgMgr.canaryCfgCancelFuncMap.remove(&canaryConfig.ObjectMeta)
			return
		}
	}
}

func (canaryCfgMgr *canaryConfigMgr) RollForwardOrBack(ctx context.Context, canaryConfig *fv1.CanaryConfig, quit chan struct{}, ticker *time.Ticker) {
	// handle race between delete event and notification on ticker.C
	_, err := canaryCfgMgr.canaryCfgCancelFuncMap.lookup(&canaryConfig.ObjectMeta)
	if err != nil {
		canaryCfgMgr.logger.Info("no need of processing the config, not in cache anymore",
			"name", canaryConfig.Name,
			"namespace", canaryConfig.Namespace,
			"version", canaryConfig.ResourceVersion)
		return
	}

	// get the http trigger object associated with this canary config
	triggerObj, err := canaryCfgMgr.fissionClient.CoreV1().HTTPTriggers(canaryConfig.ObjectMeta.Namespace).Get(ctx, canaryConfig.Spec.Trigger, metav1.GetOptions{})
	if err != nil {
		// if the http trigger is not found, then give up processing this config.
		if k8serrors.IsNotFound(err) {
			canaryCfgMgr.logger.Error(err, "http trigger object for canary config missing", "trigger", canaryConfig.Spec.Trigger,
				"name", canaryConfig.Name,
				"namespace", canaryConfig.Namespace,
				"version", canaryConfig.ResourceVersion)
			close(quit)
			return
		}

		// just silently ignore. wait for next window to increment weight
		canaryCfgMgr.logger.Error(err, "error fetching http trigger object for config", "name", canaryConfig.Name,
			"namespace", canaryConfig.Namespace,
			"version", canaryConfig.ResourceVersion)
		return
	}

	// handle a race between ticker.Stop and receiving a notification on ticker.C
	if canaryConfig.Status.Status != fv1.CanaryConfigStatusPending {
		canaryCfgMgr.logger.Info("no need of processing the config, not pending anymore",
			"name", canaryConfig.Name,
			"namespace", canaryConfig.Namespace,
			"version", canaryConfig.ResourceVersion)
		return
	}

	if triggerObj.Spec.FunctionReference.Type == fv1.FunctionReferenceTypeFunctionWeights &&
		triggerObj.Spec.FunctionReference.FunctionWeights[canaryConfig.Spec.NewFunction] != 0 {
		var urlPath string
		if triggerObj.Spec.Prefix != nil && *triggerObj.Spec.Prefix != "" {
			urlPath = *triggerObj.Spec.Prefix
		} else {
			urlPath = triggerObj.Spec.RelativeURL
		}
		methods := triggerObj.Spec.Methods
		if len(triggerObj.Spec.Method) > 0 {
			present := slices.Contains(triggerObj.Spec.Methods, triggerObj.Spec.Method)
			if !present {
				methods = append(methods, triggerObj.Spec.Method)
			}
		}
		failurePercent, err := canaryCfgMgr.promClient.GetFunctionFailurePercentage(ctx, urlPath, methods,
			canaryConfig.Spec.NewFunction, canaryConfig.Namespace, canaryConfig.Spec.WeightIncrementDuration)
		if err != nil {
			// silently ignore. wait for next window to increment weight
			canaryCfgMgr.logger.Error(err, "error calculating failure percentage", "name", canaryConfig.Name,
				"namespace", canaryConfig.Namespace,
				"version", canaryConfig.ResourceVersion)
			return
		}

		canaryCfgMgr.logger.Info("failure percentage calculated for canaryConfig",
			"failure_percent", failurePercent,
			"name", canaryConfig.Name,
			"namespace", canaryConfig.Namespace,
			"version", canaryConfig.ResourceVersion)

		if failurePercent == -1 {
			// this means there were no requests triggered to this url during this window. return here and check back
			// during next iteration
			canaryCfgMgr.logger.Info("total requests received for url is 0", "url", urlPath)
			return
		}

		if int(failurePercent) > canaryConfig.Spec.FailureThreshold {
			canaryCfgMgr.logger.Info("failure percent crossed the threshold, so rolling back",
				"failure_percent", failurePercent,
				"threshold", canaryConfig.Spec.FailureThreshold,
				"name", canaryConfig.Name,
				"namespace", canaryConfig.Namespace,
				"version", canaryConfig.ResourceVersion)
			ticker.Stop()
			err := canaryCfgMgr.rollback(ctx, canaryConfig, triggerObj)
			if err != nil {
				canaryCfgMgr.logger.Error(err, "error rolling back canary config", "name", canaryConfig.Name,
					"namespace", canaryConfig.Namespace,
					"version", canaryConfig.ResourceVersion)
			}
			close(quit)
			return
		}
	}

	doneProcessingCanaryConfig, err := canaryCfgMgr.rollForward(ctx, canaryConfig, triggerObj)
	if err != nil {
		// just log the error and hope that next iteration will succeed
		canaryCfgMgr.logger.Error(err, "error incrementing weights for trigger", "trigger", triggerObj.Name,
			"name", canaryConfig.Name,
			"namespace", canaryConfig.Namespace,
			"version", canaryConfig.ResourceVersion)
		return
	}

	if doneProcessingCanaryConfig {
		ticker.Stop()
		// update the status of canary config as done processing, we don't care if we aren't able to update because
		// resync takes care of the update
		err = canaryCfgMgr.updateCanaryConfigStatusWithRetries(ctx, canaryConfig.Name, canaryConfig.Namespace,
			fv1.CanaryConfigStatusSucceeded)
		if err != nil {
			// can't do much after max retries other than logging it.
			canaryCfgMgr.logger.Error(err, "error updating canary config after max retries", "name", canaryConfig.Name,
				"namespace", canaryConfig.Namespace,
				"version", canaryConfig.ResourceVersion)
		}

		canaryCfgMgr.logger.Info("done processing canary config - the new function is receiving all the traffic",
			"name", canaryConfig.Name,
			"namespace", canaryConfig.Namespace,
			"version", canaryConfig.ResourceVersion)
		close(quit)
		return
	}
}

func (canaryCfgMgr *canaryConfigMgr) updateHttpTriggerWithRetries(ctx context.Context, triggerName, triggerNamespace string, fnWeights map[string]int) (err error) {
	for range maxRetries {
		triggerObj, err := canaryCfgMgr.fissionClient.CoreV1().HTTPTriggers(triggerNamespace).Get(ctx, triggerName, metav1.GetOptions{})
		if err != nil {
			canaryCfgMgr.logger.Error(err, "error getting http trigger object", "trigger_name", triggerName, "trigger_namespace", triggerNamespace)
			return fmt.Errorf("error getting http trigger object: %w", err)
		}

		triggerObj.Spec.FunctionReference.FunctionWeights = fnWeights

		_, err = canaryCfgMgr.fissionClient.CoreV1().HTTPTriggers(triggerNamespace).Update(ctx, triggerObj, metav1.UpdateOptions{})
		switch {
		case err == nil:
			canaryCfgMgr.logger.V(1).Info("updated http trigger", "trigger_name", triggerName, "trigger_namespace", triggerNamespace)
			return nil
		case k8serrors.IsConflict(err):
			canaryCfgMgr.logger.Error(err, "conflict in updating http trigger, retrying", "trigger_name", triggerName,
				"trigger_namespace", triggerNamespace)
			continue
		default:
			e := "error updating http trigger"
			canaryCfgMgr.logger.Error(err, "error updating http trigger", "trigger_name", triggerName,
				"trigger_namespace", triggerNamespace)
			return fmt.Errorf("%s: %s.%s %w", e, triggerName, triggerNamespace, err)
		}
	}

	return err
}

func (canaryCfgMgr *canaryConfigMgr) updateCanaryConfigStatusWithRetries(ctx context.Context, cfgName, cfgNamespace string, status string) (err error) {
	for range maxRetries {
		canaryCfgObj, err := canaryCfgMgr.fissionClient.CoreV1().CanaryConfigs(cfgNamespace).Get(ctx, cfgName, metav1.GetOptions{})
		if err != nil {
			e := "error getting http canary config object"
			canaryCfgMgr.logger.Error(err, e, "name", cfgName,
				"namespace", cfgNamespace,
				"status", status)
			return fmt.Errorf("%s: %s.%s %w", e, cfgName, cfgNamespace, err)
		}

		canaryCfgMgr.logger.Info("updating status of canary config",
			"name", cfgName,
			"namespace", cfgNamespace,
			"status", status)

		canaryCfgObj.Status.Status = status

		_, err = canaryCfgMgr.fissionClient.CoreV1().CanaryConfigs(cfgNamespace).Update(ctx, canaryCfgObj, metav1.UpdateOptions{})
		switch {
		case err == nil:
			canaryCfgMgr.logger.Info("updated canary config",
				"name", cfgName,
				"namespace", cfgNamespace)
			return nil
		case k8serrors.IsConflict(err):
			canaryCfgMgr.logger.Info("conflict in updating canary config",
				"error", err,
				"name", cfgName,
				"namespace", cfgNamespace)
			continue
		default:
			e := "error updating canary config"
			canaryCfgMgr.logger.Error(err, e, "name", cfgName,
				"namespace", cfgNamespace)
			return fmt.Errorf("%s: %s.%s %w", e, cfgName, cfgNamespace, err)
		}
	}

	return err
}

func (canaryCfgMgr *canaryConfigMgr) rollback(ctx context.Context, canaryConfig *fv1.CanaryConfig, trigger *fv1.HTTPTrigger) error {
	functionWeights := trigger.Spec.FunctionReference.FunctionWeights
	functionWeights[canaryConfig.Spec.NewFunction] = 0
	functionWeights[canaryConfig.Spec.OldFunction] = 100

	err := canaryCfgMgr.updateHttpTriggerWithRetries(ctx, trigger.Name, trigger.Namespace, functionWeights)
	if err != nil {
		return err
	}

	err = canaryCfgMgr.updateCanaryConfigStatusWithRetries(ctx, canaryConfig.Name, canaryConfig.Namespace,
		fv1.CanaryConfigStatusFailed)

	return err
}

func (canaryCfgMgr *canaryConfigMgr) rollForward(ctx context.Context, canaryConfig *fv1.CanaryConfig, trigger *fv1.HTTPTrigger) (bool, error) {
	doneProcessingCanaryConfig := false

	functionWeights := trigger.Spec.FunctionReference.FunctionWeights
	if functionWeights[canaryConfig.Spec.NewFunction]+canaryConfig.Spec.WeightIncrement >= 100 {
		doneProcessingCanaryConfig = true
		functionWeights[canaryConfig.Spec.NewFunction] = 100
		functionWeights[canaryConfig.Spec.OldFunction] = 0
	} else {
		functionWeights[canaryConfig.Spec.NewFunction] += canaryConfig.Spec.WeightIncrement
		if functionWeights[canaryConfig.Spec.OldFunction]-canaryConfig.Spec.WeightIncrement < 0 {
			functionWeights[canaryConfig.Spec.OldFunction] = 0
		} else {
			functionWeights[canaryConfig.Spec.OldFunction] -= canaryConfig.Spec.WeightIncrement
		}
	}

	canaryCfgMgr.logger.Info("incremented functionWeights",
		"name", canaryConfig.Name,
		"namespace", canaryConfig.Namespace,
		"function_weights", functionWeights)

	err := canaryCfgMgr.updateHttpTriggerWithRetries(ctx, trigger.Name, trigger.Namespace, functionWeights)
	return doneProcessingCanaryConfig, err
}

func (canaryCfgMgr *canaryConfigMgr) reSyncCanaryConfigs(ctx context.Context) {
	for _, informer := range canaryCfgMgr.canaryConfigInformer {
		for _, obj := range informer.GetStore().List() {
			canaryConfig := obj.(*fv1.CanaryConfig)
			_, err := canaryCfgMgr.canaryCfgCancelFuncMap.lookup(&canaryConfig.ObjectMeta)
			if err != nil && canaryConfig.Status.Status == fv1.CanaryConfigStatusPending {
				canaryCfgMgr.logger.Info("adding canary config from resync loop",
					"name", canaryConfig.Name,
					"namespace", canaryConfig.Namespace,
					"version", canaryConfig.ResourceVersion)

				// new canaryConfig detected, add it to our cache and start processing it
				go canaryCfgMgr.addCanaryConfig(ctx, canaryConfig)
			}
		}
	}
}

func (canaryCfgMgr *canaryConfigMgr) deleteCanaryConfig(canaryConfig *fv1.CanaryConfig) {
	canaryCfgMgr.logger.V(1).Info("delete event received for canary config",
		"name", canaryConfig.Name,
		"namespace", canaryConfig.Namespace,
		"version", canaryConfig.ResourceVersion)
	canaryProcessingInfo, err := canaryCfgMgr.canaryCfgCancelFuncMap.lookup(&canaryConfig.ObjectMeta)
	if err != nil {
		canaryCfgMgr.logger.Error(err, "lookup of canary config for deletion failed", "name", canaryConfig.Name,
			"namespace", canaryConfig.Namespace,
			"version", canaryConfig.ResourceVersion)
		return
	}
	// first stop the ticker
	canaryProcessingInfo.Ticker.Stop()
	// call cancel func so that the ctx.Done returns inside processCanaryConfig function and processing gets stopped
	(*canaryProcessingInfo.CancelFunc)()
}

func (canaryCfgMgr *canaryConfigMgr) updateCanaryConfig(ctx context.Context, oldCanaryConfig *fv1.CanaryConfig, newCanaryConfig *fv1.CanaryConfig) {
	// before removing the object from cache, we need to get it's cancel func and cancel it
	canaryCfgMgr.deleteCanaryConfig(oldCanaryConfig)

	canaryCfgMgr.canaryCfgCancelFuncMap.remove(&oldCanaryConfig.ObjectMeta)
	canaryCfgMgr.addCanaryConfig(ctx, newCanaryConfig)
}

func getEnvValue(envVar string) string {
	envVarSplit := strings.Split(envVar, "=")
	return envVarSplit[1]
}

func StartCanaryServer(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr manager.Interface, unitTestFlag bool) error {
	cLogger := logger.WithName("CanaryServer")

	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("failed to get fission client: %w", err)
	}
	kubernetesClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to get kubernetes client: %w", err)
	}

	err = ConfigureFeatures(ctx, cLogger, unitTestFlag, fissionClient, kubernetesClient, mgr)
	if err != nil {
		cLogger.Error(err, "error configuring features - proceeding without optional features")
	}
	return err
}
