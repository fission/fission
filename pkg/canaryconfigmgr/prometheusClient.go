// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfigmgr

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	prometheus "github.com/prometheus/client_golang/api"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

type PrometheusApiClient struct {
	logger logr.Logger
	client prometheusv1.API
}

func MakePrometheusClient(logger logr.Logger, prometheusSvc string) (*PrometheusApiClient, error) {
	promApiConfig := prometheus.Config{
		Address: prometheusSvc,
	}

	promApiClient, err := prometheus.NewClient(promApiConfig)
	if err != nil {
		return nil, fmt.Errorf("error creating prometheus api client for svc: %s: %w", prometheusSvc, err)
	}

	apiQueryClient := prometheusv1.NewAPI(promApiClient)

	return &PrometheusApiClient{
		logger: logger.WithName("prometheus_api_client"),
		client: apiQueryClient,
	}, nil
}

// funcVersion (RFC-0025 phase 5) adds a function_version label to every query
// this method issues, disambiguating two versions of the SAME function that
// otherwise share every other label (function_name/function_namespace/path/
// method) — see docs/rfc/0025-function-versions-aliases-rollback.md
// L181-182. Empty is function-pair mode: the query is byte-identical to
// before this parameter existed.
func (promApiClient *PrometheusApiClient) GetFunctionFailurePercentage(ctx context.Context, path string, methods []string, funcName, funcVersion, funcNs string, window string) (float64, error) {
	var reqs, failedReqs float64
	// first get a total count of requests to this url in a time window
	for _, method := range methods {
		mreqs, err := promApiClient.GetRequestsToFuncInWindow(ctx, path, method, funcName, funcVersion, funcNs, window)
		if err != nil {
			return 0, err
		}
		reqs += mreqs
	}

	if reqs <= 0 {
		return -1, fmt.Errorf("no requests to this url %s and method %v in the window: %s", path, methods, window)
	}

	// next, get a total count of errored out requests to this function in the same window
	for _, method := range methods {
		mfailedReqs, err := promApiClient.GetTotalFailedRequestsToFuncInWindow(ctx, funcName, funcVersion, funcNs, path, method, window)
		if err != nil {
			return 0, err
		}
		failedReqs += mfailedReqs
	}

	// calculate the failure percentage of the function
	failurePercentForFunc := (failedReqs / reqs) * 100

	return failurePercentForFunc, nil
}

// getFunctionQueryLabels builds the PromQL label-matcher body shared by every
// query this client issues. functionVersion is only appended when non-empty
// (pair mode omits it, keeping the query byte-identical to the pre-alias-mode
// shape); see GetFunctionFailurePercentage.
func (PrometheusApiClient *PrometheusApiClient) getFunctionQueryLabels(functionName, functionVersion, functionNamespace, path, method string) string {
	labels := fmt.Sprintf("function_name=\"%s\",function_namespace=\"%s\",path=\"%s\",method=\"%s\"", functionName, functionNamespace, path, method)
	if functionVersion != "" {
		labels += fmt.Sprintf(",function_version=\"%s\"", functionVersion)
	}
	return labels
}

func (promApiClient *PrometheusApiClient) GetRequestsToFuncInWindow(ctx context.Context, path string, method string, funcName string, funcVersion string, funcNs string, window string) (float64, error) {
	queryLabels := promApiClient.getFunctionQueryLabels(funcName, funcVersion, funcNs, path, method)
	queryString := fmt.Sprintf("fission_function_calls_total{%s}[%v]", queryLabels, window)

	reqs, err := promApiClient.executeQuery(ctx, queryString)
	if err != nil {
		return 0, fmt.Errorf("error executing query %s: %w", queryString, err)
	}

	queryString = fmt.Sprintf("fission_function_calls_total{%s} offset %v", queryLabels, window)

	reqsInPrevWindow, err := promApiClient.executeQuery(ctx, queryString)
	if err != nil {
		return 0, fmt.Errorf("error executing query %s: %w", queryString, err)
	}

	reqsInCurrentWindow := reqs - reqsInPrevWindow
	promApiClient.logger.Info("function requests",
		"requests", reqs,
		"requests_in_previous_window", reqsInPrevWindow,
		"requests_in_current_window", reqsInCurrentWindow,
		"function", funcName)

	return reqsInCurrentWindow, nil
}

func (promApiClient *PrometheusApiClient) GetTotalFailedRequestsToFuncInWindow(ctx context.Context, funcName string, funcVersion string, funcNs string, path string, method string, window string) (float64, error) {
	queryLabels := promApiClient.getFunctionQueryLabels(funcName, funcVersion, funcNs, path, method)
	queryString := fmt.Sprintf("fission_function_errors_total{%s}[%v]", queryLabels, window)

	failedRequests, err := promApiClient.executeQuery(ctx, queryString)
	if err != nil {
		return 0, fmt.Errorf("error executing query %s: %w", queryString, err)
	}

	queryString = fmt.Sprintf("fission_function_errors_total{%s} offset %v", queryLabels, window)

	failedReqsInPrevWindow, err := promApiClient.executeQuery(ctx, queryString)
	if err != nil {
		return 0, fmt.Errorf("error executing query %s: %w", queryString, err)
	}

	failedReqsInCurrentWindow := failedRequests - failedReqsInPrevWindow
	promApiClient.logger.Info("function requests",
		"failed_requests", failedRequests,
		"failed_requests_in_previous_window", failedReqsInPrevWindow,
		"failed_requests_in_current_window", failedReqsInCurrentWindow,
		"function", funcName)

	return failedReqsInCurrentWindow, nil
}

func (promApiClient *PrometheusApiClient) executeQuery(ctx context.Context, queryString string) (float64, error) {
	promApiClient.logger.V(1).Info("executing prometheus query", "query", queryString)

	val, warn, err := promApiClient.client.Query(ctx, queryString, time.Now())
	if err != nil {
		return 0, fmt.Errorf("error querying prometheus: %w", err)
	}

	if warn != nil {
		promApiClient.logger.Info("receive prometheus client query warning", "msg", warn)
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
			"type", val.Type())
		return 0, nil
	}
}
