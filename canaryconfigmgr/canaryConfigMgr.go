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
	"fmt"
	"os"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	k8sCache "k8s.io/client-go/tools/cache"

	"github.com/fission/fission"
	"github.com/fission/fission/crd"
)

type canaryConfigMgr struct {
	fissionClient          *crd.FissionClient
	kubeClient             *kubernetes.Clientset
	canaryConfigStore      k8sCache.Store
	canaryConfigController k8sCache.Controller
	promClient             *PrometheusApiClient
	crdClient              *rest.RESTClient
	canaryCfgCancelFuncMap *canaryConfigCancelFuncMap
}

func MakeCanaryConfigMgr(fissionClient *crd.FissionClient, kubeClient *kubernetes.Clientset, crdClient *rest.RESTClient, prometheusSvc string) (*canaryConfigMgr, error) {
	if prometheusSvc == "" {
		// handle a case where there is a prometheus server is already installed, try to find the service from env variable
		envVars := os.Environ()
		for _, envVar := range envVars {
			if strings.Contains(envVar, "PROMETHEUS_SERVER_SERVICE_HOST") {
				envVarSplit := strings.Split(envVar, "=")
				prometheusSvc = envVarSplit[1]
				break
			}
		}

		if prometheusSvc == "" {
			return nil, fmt.Errorf("prometheus service not found, cant create canary config manager")
		}
	}

	configMgr := &canaryConfigMgr{
		fissionClient:          fissionClient,
		kubeClient:             kubeClient,
		crdClient:              crdClient,
		promClient:             MakePrometheusClient(prometheusSvc),
		canaryCfgCancelFuncMap: makecanaryConfigCancelFuncMap(),
	}

	store, controller := configMgr.initCanaryConfigController()
	configMgr.canaryConfigStore = store
	configMgr.canaryConfigController = controller

	return configMgr, nil
}

func (canaryCfgMgr *canaryConfigMgr) initCanaryConfigController() (k8sCache.Store, k8sCache.Controller) {
	resyncPeriod := 30 * time.Second
	listWatch := k8sCache.NewListWatchFromClient(canaryCfgMgr.crdClient, "canaryconfigs", metav1.NamespaceAll, fields.Everything())
	store, controller := k8sCache.NewInformer(listWatch, &crd.CanaryConfig{}, resyncPeriod,
		k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				canaryConfig := obj.(*crd.CanaryConfig)
				if canaryConfig.Status.Status == fission.CanaryConfigStatusPending {
					go canaryCfgMgr.addCanaryConfig(canaryConfig)
				}
			},
			DeleteFunc: func(obj interface{}) {
				canaryConfig := obj.(*crd.CanaryConfig)
				go canaryCfgMgr.deleteCanaryConfig(canaryConfig)
			},
			UpdateFunc: func(oldObj interface{}, newObj interface{}) {
				oldConfig := oldObj.(*crd.CanaryConfig)
				newConfig := newObj.(*crd.CanaryConfig)
				if oldConfig.Metadata.ResourceVersion != newConfig.Metadata.ResourceVersion &&
					newConfig.Status.Status == fission.CanaryConfigStatusPending {
					log.Printf("update canary config invoked for : %s.%s, newConfig.Status.Status=%s", newConfig.Metadata.Name, newConfig.Metadata.Namespace, newConfig.Status.Status)
					go canaryCfgMgr.updateCanaryConfig(oldConfig, newConfig)
				}
				go canaryCfgMgr.reSyncCanaryConfigs()

			},
		})

	return store, controller
}

func (canaryCfgMgr *canaryConfigMgr) Run(ctx context.Context) {
	go canaryCfgMgr.canaryConfigController.Run(ctx.Done())
	log.Printf("started Canary configmgr controller")
}

func (canaryCfgMgr *canaryConfigMgr) addCanaryConfig(canaryConfig *crd.CanaryConfig) {
	log.Printf("addCanaryConfig called for %s", canaryConfig.Metadata.Name)

	// for each canary config, create a ticker with increment interval
	interval, err := time.ParseDuration(canaryConfig.Spec.WeightIncrementDuration)
	if err != nil {
		log.Printf("Error parsing duration: %v, cant proceed with this canaryConfig : %v.%v", err,
			canaryConfig.Metadata.Name, canaryConfig.Metadata.Namespace)
		return
	}
	ticker := time.NewTicker(interval)

	// create a context cancel func for each canary config. this will be used to cancel the processing of this canary
	// config in the event that it's deleted
	ctx, cancel := context.WithCancel(context.Background())

	cacheValue := &CanaryProcessingInfo{
		CancelFunc: &cancel,
		Ticker:     ticker,
	}
	err = canaryCfgMgr.canaryCfgCancelFuncMap.assign(&canaryConfig.Metadata, cacheValue)
	if err != nil {
		log.Printf("Error caching canary config : %s.%s. err : %v", canaryConfig.Metadata.Name, canaryConfig.Metadata.Namespace, err)
		return
	}
	canaryCfgMgr.processCanaryConfig(&ctx, canaryConfig, ticker)
}

func (canaryCfgMgr *canaryConfigMgr) processCanaryConfig(ctx *context.Context, canaryConfig *crd.CanaryConfig, ticker *time.Ticker) {
	quit := make(chan struct{})

	for {
		select {
		case <-(*ctx).Done():
			// this case when someone deleted their canary config in the middle of it being processed
			log.Printf("Cancel Func called for canary config : %s", canaryConfig.Metadata.Name)
			err := canaryCfgMgr.canaryCfgCancelFuncMap.remove(&canaryConfig.Metadata)
			if err != nil {
				log.Printf("error removing canary config: %s from map, err : %v", canaryConfig.Metadata.Name, err)
			}
			return

		case <-ticker.C:
			// every weightIncrementDuration, check if failureThreshold has reached.
			// if yes, rollback.
			// else, increment the weight of new function and decrement old function by `weightIncrement`
			log.Printf("Processing canary config : %s.%s", canaryConfig.Metadata.Name, canaryConfig.Metadata.Namespace)
			canaryCfgMgr.RollForwardOrBack(canaryConfig, quit, ticker)

		case <-quit:
			// we're done processing this canary config either because the new function receives 100% of the traffic
			// or we rolled back to send all 100% traffic to old function
			log.Printf("Quit processing canaryConfig : %s", canaryConfig.Metadata.Name)
			err := canaryCfgMgr.canaryCfgCancelFuncMap.remove(&canaryConfig.Metadata)
			if err != nil {
				log.Printf("error removing canary config: %s from map, err : %v", canaryConfig.Metadata.Name, err)
			}
			return
		}
	}
}

func (canaryCfgMgr *canaryConfigMgr) RollForwardOrBack(canaryConfig *crd.CanaryConfig, quit chan struct{}, ticker *time.Ticker) {
	// handle race between delete event and notification on ticker.C
	_, err := canaryCfgMgr.canaryCfgCancelFuncMap.lookup(&canaryConfig.Metadata)
	if err != nil {
		log.Printf("No need of processing the config, not in cache anymore")
		return
	}

	// get the http trigger object associated with this canary config
	triggerObj, err := canaryCfgMgr.fissionClient.HTTPTriggers(canaryConfig.Metadata.Namespace).Get(canaryConfig.Spec.Trigger)
	if err != nil {
		// if the http trigger is not found, then give up processing this config.
		if k8serrors.IsNotFound(err) {
			log.Printf("Http trigger object : %v.%v missing", canaryConfig.Spec.Trigger, canaryConfig.Metadata.Namespace)
			close(quit)
			return
		}

		// just silently ignore. wait for next window to increment weight
		log.Printf("Error fetching http trigger object, err : %v", err)
		return
	}

	// handle a race between ticker.Stop and receiving a notification on ticker.C
	if canaryConfig.Status.Status != fission.CanaryConfigStatusPending {
		log.Printf("No need of processing the config, not pending anymore")
		return
	}

	if triggerObj.Spec.FunctionReference.Type == fission.FunctionReferenceTypeFunctionWeights &&
		triggerObj.Spec.FunctionReference.FunctionWeights[canaryConfig.Spec.NewFunction] != 0 {
		failurePercent, err := canaryCfgMgr.promClient.GetFunctionFailurePercentage(triggerObj.Spec.RelativeURL, triggerObj.Spec.Method,
			canaryConfig.Spec.NewFunction, canaryConfig.Metadata.Namespace, canaryConfig.Spec.WeightIncrementDuration)

		if err != nil {
			// silently ignore. wait for next window to increment weight
			log.Printf("Error calculating failure percentage, err : %v", err)
			return
		}

		log.Printf("Failure percentage calculated : %v for canaryConfig %s", failurePercent, canaryConfig.Metadata.Name)
		if failurePercent == -1 {
			// this means there were no requests triggered to this url during this window. return here and check back
			// during next iteration
			log.Printf("Total requests received for url : %v is 0", triggerObj.Spec.RelativeURL)
			return
		}

		if int(failurePercent) > canaryConfig.Spec.FailureThreshold {
			log.Printf("Failure percent %v crossed the threshold %v, so rolling back", failurePercent, canaryConfig.Spec.FailureThreshold)
			ticker.Stop()
			canaryCfgMgr.rollback(canaryConfig, triggerObj)
			close(quit)
			return
		}
	}

	doneProcessingCanaryConfig, err := canaryCfgMgr.rollForward(canaryConfig, triggerObj)
	if err != nil {
		// just log the error and hope that next iteration will succeed
		log.Printf("Error incrementing weights for triggerObj : %v, err : %v", triggerObj.Metadata.Name, err)
		return
	}

	if doneProcessingCanaryConfig {
		ticker.Stop()
		// update the status of canary config as done processing, we dont care if we arent able to update because
		// resync takes care of the update
		err = canaryCfgMgr.updateCanaryConfigStatusWithRetries(canaryConfig.Metadata.Name, canaryConfig.Metadata.Namespace,
			fission.CanaryConfigStatusSucceeded)
		if err != nil {
			// cant do much after max retries other than logging it.
			log.Printf("Error updating canary config : %s.%s after max retries, err :%v", canaryConfig.Metadata.Name, canaryConfig.Metadata.Namespace,
				err)
		}

		log.Printf("We're done processing canary config : %s. The new function is receiving all the traffic", canaryConfig.Metadata.Name)
		close(quit)
		return
	}
}

func (canaryCfgMgr *canaryConfigMgr) updateHttpTriggerWithRetries(triggerName, triggerNamespace string, fnWeights map[string]int) (err error) {
	for i := 0; i < fission.MaxRetries; i++ {
		triggerObj, err := canaryCfgMgr.fissionClient.HTTPTriggers(triggerNamespace).Get(triggerName)
		if err != nil {
			log.Printf("Error getting http trigger object : %v", err)
			return err
		}

		triggerObj.Spec.FunctionReference.FunctionWeights = fnWeights

		_, err = canaryCfgMgr.fissionClient.HTTPTriggers(triggerNamespace).Update(triggerObj)
		switch {
		case err == nil:
			log.Printf("Updated Http trigger : %s.%s", triggerName, triggerNamespace)
			return nil
		case k8serrors.IsConflict(err):
			log.Printf("Conflict in updating http trigger : %s.%s, retrying", triggerName, triggerNamespace)
			continue
		default:
			log.Printf("Error updating trigger : %s.%s = %v", triggerName, triggerNamespace, err)
			return err
		}
	}

	return err
}

func (canaryCfgMgr *canaryConfigMgr) updateCanaryConfigStatusWithRetries(cfgName, cfgNamespace string, status string) (err error) {
	for i := 0; i < fission.MaxRetries; i++ {
		canaryCfgObj, err := canaryCfgMgr.fissionClient.CanaryConfigs(cfgNamespace).Get(cfgName)
		if err != nil {
			log.Printf("Error getting http Canary Config object : %v", err)
			return err
		}

		log.Printf("Updating status of canaryCfg : %s.%s to %s", cfgName, cfgNamespace, status)
		canaryCfgObj.Status.Status = status

		_, err = canaryCfgMgr.fissionClient.CanaryConfigs(cfgNamespace).Update(canaryCfgObj)
		switch {
		case err == nil:
			log.Printf("Updated Canary Config : %s.%s", cfgName, cfgNamespace)
			return nil
		case k8serrors.IsConflict(err):
			log.Printf("Conflict in updating Canary Config : %s.%s, retrying", cfgName, cfgNamespace)
			continue
		default:
			log.Printf("Error updating Canary Config : %s.%s = %v", cfgName, cfgNamespace, err)
			return err
		}
	}

	return err
}

func (canaryCfgMgr *canaryConfigMgr) rollback(canaryConfig *crd.CanaryConfig, trigger *crd.HTTPTrigger) error {
	functionWeights := trigger.Spec.FunctionReference.FunctionWeights
	functionWeights[canaryConfig.Spec.NewFunction] = 0
	functionWeights[canaryConfig.Spec.OldFunction] = 100

	err := canaryCfgMgr.updateHttpTriggerWithRetries(trigger.Metadata.Name, trigger.Metadata.Namespace, functionWeights)

	err = canaryCfgMgr.updateCanaryConfigStatusWithRetries(canaryConfig.Metadata.Name, canaryConfig.Metadata.Namespace,
		fission.CanaryConfigStatusFailed)

	return err
}

func (canaryCfgMgr *canaryConfigMgr) rollForward(canaryConfig *crd.CanaryConfig, trigger *crd.HTTPTrigger) (bool, error) {
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

	log.Printf("Incremented functionWeights : %v", functionWeights)

	err := canaryCfgMgr.updateHttpTriggerWithRetries(trigger.Metadata.Name, trigger.Metadata.Namespace, functionWeights)
	return doneProcessingCanaryConfig, err
}

func (canaryCfgMgr *canaryConfigMgr) reSyncCanaryConfigs() {
	for _, obj := range canaryCfgMgr.canaryConfigStore.List() {
		canaryConfig := obj.(*crd.CanaryConfig)
		_, err := canaryCfgMgr.canaryCfgCancelFuncMap.lookup(&canaryConfig.Metadata)
		if err != nil && canaryConfig.Status.Status == fission.CanaryConfigStatusPending {
			log.Printf("Adding canary config : %s.%s from resync loop", canaryConfig.Metadata.Name, canaryConfig.Metadata.Namespace)

			// new canaryConfig detected, add it to our cache and start processing it
			go canaryCfgMgr.addCanaryConfig(canaryConfig)
		}
	}
}

func (canaryCfgMgr *canaryConfigMgr) deleteCanaryConfig(canaryConfig *crd.CanaryConfig) {
	log.Printf("Delete event received for canary config : %v, %v, %v", canaryConfig.Metadata.Name, canaryConfig.Metadata.Namespace, canaryConfig.Metadata.ResourceVersion)
	canaryProcessingInfo, err := canaryCfgMgr.canaryCfgCancelFuncMap.lookup(&canaryConfig.Metadata)
	if err != nil {
		log.Printf("lookup of canaryConfig failed, err : %v", err)
		return
	}
	// first stop the ticker
	canaryProcessingInfo.Ticker.Stop()
	// call cancel func so that the ctx.Done returns inside processCanaryConfig function and processing gets stopped
	(*canaryProcessingInfo.CancelFunc)()
}

func (canaryCfgMgr *canaryConfigMgr) updateCanaryConfig(oldCanaryConfig *crd.CanaryConfig, newCanaryConfig *crd.CanaryConfig) {
	// before removing the object from cache, we need to get it's cancel func and cancel it
	canaryCfgMgr.deleteCanaryConfig(oldCanaryConfig)

	err := canaryCfgMgr.canaryCfgCancelFuncMap.remove(&oldCanaryConfig.Metadata)
	if err != nil {
		log.Printf("error removing canary config: %s from map, err : %v", oldCanaryConfig.Metadata.Name, err)
		return
	}
	canaryCfgMgr.addCanaryConfig(newCanaryConfig)
}
