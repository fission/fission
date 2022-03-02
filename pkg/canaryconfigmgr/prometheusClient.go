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
	"time"

	"github.com/pkg/errors"
	prometheus "github.com/prometheus/client_golang/api"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"go.uber.org/zap"
)

type PrometheusApiClient struct {
	logger *zap.Logger
	client prometheusv1.API
}

func MakePrometheusClient(logger *zap.Logger, prometheusSvc string) (*PrometheusApiClient, error) {
	promApiConfig := prometheus.Config{
		Address: prometheusSvc,
	}

	promApiClient, err := prometheus.NewClient(promApiConfig)
	if err != nil {
		return nil, errors.Wrapf(err, "error creating prometheus api client for svc: %s", prometheusSvc)
	}

	apiQueryClient := prometheusv1.NewAPI(promApiClient)

	return &PrometheusApiClient{
		logger: logger.Named("prometheus_api_client"),
		client: apiQueryClient,
	}, nil
}

func (promApiClient *PrometheusApiClient) GetFunctionFailurePercentage(path string, methods []string, funcName, funcNs string, window string) (float64, error) {
	var reqs, failedReqs float64
	// first get a total count of requests to this url in a time window
	for _, method := range methods {
		mreqs, err := promApiClient.GetRequestsToFuncInWindow(path, method, funcName, funcNs, window)
		if err != nil {
			return 0, err
		}
		reqs += mreqs
	}

	if reqs <= 0 {
		return -1, fmt.Errorf("no requests to this url %v and method %v in the window: %v", path, methods, window)
	}

	// next, get a total count of errored out requests to this function in the same window
	for _, method := range methods {
		mfailedReqs, err := promApiClient.GetTotalFailedRequestsToFuncInWindow(funcName, funcNs, path, method, window)
		if err != nil {
			return 0, err
		}
		failedReqs += mfailedReqs
	}

	// calculate the failure percentage of the function
	failurePercentForFunc := (failedReqs / reqs) * 100

	return failurePercentForFunc, nil
}

func (PrometheusApiClient *PrometheusApiClient) getFunctionQueryLabels(functionName, functionNamespace, path, method string) string {
	return fmt.Sprintf("function_name=\"%s\",function_namespace=\"%s\",path=\"%s\",method=\"%s\"", functionName, functionNamespace, path, method)
}

func (promApiClient *PrometheusApiClient) GetRequestsToFuncInWindow(path string, method string, funcName string, funcNs string, window string) (float64, error) {
	queryLabels := promApiClient.getFunctionQueryLabels(funcName, funcNs, path, method)
	queryString := fmt.Sprintf("fission_function_calls_total{%s}[%v]", queryLabels, window)

	reqs, err := promApiClient.executeQuery(queryString)
	if err != nil {
		return 0, errors.Wrapf(err, "error executing query: %s", queryString)
	}

	queryString = fmt.Sprintf("fission_function_calls_total{%s} offset %v", queryLabels, window)

	reqsInPrevWindow, err := promApiClient.executeQuery(queryString)
	if err != nil {
		return 0, errors.Wrapf(err, "error executing query: %s", queryString)
	}

	reqsInCurrentWindow := reqs - reqsInPrevWindow
	promApiClient.logger.Info("function requests",
		zap.Float64("requests", reqs),
		zap.Float64("requests_in_previous_window", reqsInPrevWindow),
		zap.Float64("requests_in_current_window", reqsInCurrentWindow),
		zap.String("function", funcName))

	return reqsInCurrentWindow, nil
}

func (promApiClient *PrometheusApiClient) GetTotalFailedRequestsToFuncInWindow(funcName string, funcNs string, path string, method string, window string) (float64, error) {
	queryLabels := promApiClient.getFunctionQueryLabels(funcName, funcNs, path, method)
	queryString := fmt.Sprintf("fission_function_errors_total{%s}[%v]", queryLabels, window)

	failedRequests, err := promApiClient.executeQuery(queryString)
	if err != nil {
		return 0, errors.Wrapf(err, "error executing query: %s", queryString)
	}

	queryString = fmt.Sprintf("fission_function_errors_total{%s} offset %v", queryLabels, window)

	failedReqsInPrevWindow, err := promApiClient.executeQuery(queryString)
	if err != nil {
		return 0, errors.Wrapf(err, "error executing query: %s", queryString)
	}

	failedReqsInCurrentWindow := failedRequests - failedReqsInPrevWindow
	promApiClient.logger.Info("function requests",
		zap.Float64("failed_requests", failedRequests),
		zap.Float64("failed_requests_in_previous_window", failedReqsInPrevWindow),
		zap.Float64("failed_requests_in_current_window", failedReqsInCurrentWindow),
		zap.String("function", funcName))

	return failedReqsInCurrentWindow, nil
}

func (promApiClient *PrometheusApiClient) executeQuery(queryString string) (float64, error) {
	promApiClient.logger.Debug("executing prometheus query", zap.String("query", queryString))

	val, warn, err := promApiClient.client.Query(context.Background(), queryString, time.Now())
	if err != nil {
		return 0, errors.Wrapf(err, "error querying prometheus")
	}

	if warn != nil {
		promApiClient.logger.Warn("receive prometheus client query warning", zap.Any("msg", warn))
	}

	switch {
	case val.Type() == model.ValScalar:
		scalarVal := val.(*model.Scalar)
		return float64(scalarVal.Value), nil

	case val.Type() == model.ValVector:
		vectorVal := val.(model.Vector)
		total := float64(0)
		for _, elem := range vectorVal {
			total = total + float64(elem.Value)
		}
		return total, nil

	case val.Type() == model.ValMatrix:
		matrixVal := val.(model.Matrix)
		total := float64(0)
		for _, elem := range matrixVal {
			total += float64(elem.Values[len(elem.Values)-1].Value)
		}
		return total, nil

	default:
		promApiClient.logger.Info("return value type of prometheus query was unrecognized",
			zap.Any("type", val.Type()))
		return 0, nil
	}
}
