// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"fmt"
	"math"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils"
)

func (gp *GenericPool) checkMetricsApi() bool {
	apiGroups, err := gp.metricsClient.Discovery().ServerGroups()
	if err != nil {
		gp.logger.Error(err, "failed to discover API groups")
		return false
	}
	return utils.SupportedMetricsAPIVersionAvailable(apiGroups)
}

func (gp *GenericPool) updateCPUUtilizationSvc(ctx context.Context) {
	var metricsApiAvailabe bool
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	if !gp.checkMetricsApi() {
		ticker.Reset(180 * time.Second)
		gp.logger.Info("Metrics API not available")
	}

	serviceFunc := func(ctx context.Context) {
		podMetricsList, err := gp.metricsClient.MetricsV1beta1().PodMetricses(gp.fnNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "managed=false",
		})
		if err != nil {
			gp.logger.Error(err, "failed to fetch pod metrics list")
			return
		}
		gp.logger.V(1).Info("pods found", "length", len(podMetricsList.Items))
		for _, val := range podMetricsList.Items {
			p, _ := resource.ParseQuantity("0m")
			for _, container := range val.Containers {
				p.Add(container.Usage["cpu"])
			}
			if value, ok := gp.podFSVCMap.Load(val.Name); ok {
				if valArray, ok1 := value.([]any); ok1 {
					function, ok2 := valArray[0].(crd.CacheKeyUG)
					if !ok2 {
						gp.logger.Error(nil, "failed to convert function to type", "function", function)
						return
					}
					address, ok2 := valArray[1].(string)
					if !ok2 {
						gp.logger.Error(nil, "failed to convert address to string", "address", address)
						return
					}
					gp.fsCache.SetCPUUtilizaton(function, address, p)
					gp.logger.Info("updated function cpu usage", "function", function, "address", address, "cpuUsage", p)
				}
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if metricsApiAvailabe {
				serviceFunc(ctx)
			} else {
				if gp.checkMetricsApi() {
					metricsApiAvailabe = true
					ticker.Reset(30 * time.Second)
				}
			}
		}
	}
}

// getPercent returns  x percent of the quantity i.e multiple it x/100
func (gp *GenericPool) getPercent(cpuUsage resource.Quantity, percentage float64) (resource.Quantity, error) {
	val := int64(math.Ceil(float64(cpuUsage.MilliValue()) * percentage))
	return resource.ParseQuantity(fmt.Sprintf("%dm", val))
}
