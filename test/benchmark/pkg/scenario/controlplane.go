// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"fmt"
	"maps"
	"time"

	apiv1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/benchmark/pkg/harness"
	"github.com/fission/fission/test/benchmark/pkg/report"
)

const benchSyntheticLabel = "fission-bench-synthetic"

// routerIndexScale creates many synthetic headless Services + EndpointSlices
// shaped like the executor's per-function objects (the labels the router's
// filtered informer selects on) WITHOUT running pods, isolating the router-side
// scale story — informer cache, index memory, admission. It then snapshots
// router footprint via pprof/Prometheus.
type routerIndexScale struct {
	count int
}

func (r *routerIndexScale) Name() string   { return "router-index-scale" }
func (r *routerIndexScale) Tags() []string { return []string{"controlplane", "scale"} }

func (r *routerIndexScale) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	env := sc.Env()
	ns := env.Namespace
	tag := env.RunID

	snapshotPprof(ctx, env, r.Name(), "before")

	// Register the bulk (label-selected) cleanup before creating anything, so a
	// mid-loop failure or ctx cancel still tears down what was created.
	sc.Defer("synthetic endpoints", func(c context.Context) error {
		return deleteSynthetic(c, env, ns, tag)
	})
	start := time.Now()
	created := 0
	for i := range r.count {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if err := createSyntheticEndpoint(ctx, env, ns, tag, i); err != nil {
			return res, fmt.Errorf("create synthetic endpoint %d: %w", i, err)
		}
		created++
	}
	res.Add("objects", "count", report.Higher, float64(created))
	res.Add("create_seconds", "s", report.Lower, time.Since(start).Seconds())

	// Let the router's informer catch up, then capture footprint.
	time.Sleep(20 * time.Second)
	snapshotPprof(ctx, env, r.Name(), "after")
	if v, found, err := env.Capturer.QueryInstant(ctx, routerRSSBytes); err == nil && found {
		res.Add("router_rss_mb", "MiB", report.Lower, v/(1024*1024))
	}
	return res, nil
}

// routeChurn registers many HTTPTriggers against a single function to exercise
// the router's route-table reconcile / mux path at scale (RFC-0013).
type routeChurn struct {
	count int
}

func (r *routeChurn) Name() string   { return "route-churn" }
func (r *routeChurn) Tags() []string { return []string{"controlplane", "scale", "routes"} }

func (r *routeChurn) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	env := sc.Env()
	if env.Images.Python == "" {
		return res, skip("PYTHON_RUNTIME_IMAGE unset")
	}

	// One real function all the synthetic routes point at, so each route is
	// admitted (resolves to a function) and actually enters the route table.
	envName := sc.Name("churn-env")
	if err := sc.CreateEnv(ctx, harness.EnvOptions{Name: envName, Image: env.Images.Python, Version: 1, Poolsize: 1}); err != nil {
		return res, err
	}
	fnName := sc.Name("churn-fn")
	if err := sc.CreateCodeFunction(ctx, harness.FunctionOptions{Name: fnName, Env: envName, Code: []byte(pythonHello), Entrypoint: "main"}); err != nil {
		return res, err
	}

	start := time.Now()
	created := 0
	for i := range r.count {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		name := fmt.Sprintf("%s-churn-%d", sc.Name("rt"), i)
		if err := sc.CreateRoute(ctx, harness.RouteOptions{
			Name: name, Function: fnName, URL: fmt.Sprintf("/churn-%s-%d", env.RunID, i),
		}); err != nil {
			return res, fmt.Errorf("create trigger %d: %w", i, err)
		}
		created++
	}
	res.Add("routes", "count", report.Higher, float64(created))
	res.Add("create_seconds", "s", report.Lower, time.Since(start).Seconds())

	time.Sleep(10 * time.Second)
	if v, found, err := env.Capturer.QueryInstant(ctx, routerRouteApplies); err == nil && found {
		res.Add("route_table_applies_total", "count", report.Lower, v)
	}
	return res, nil
}

// routerRSSBytes / routerRouteApplies are best-effort PromQL probes; absent
// metrics yield 0 and are simply omitted.
const (
	routerRSSBytes     = `max(process_resident_memory_bytes{job=~".*router.*"})`
	routerRouteApplies = `sum(fission_router_route_table_applies_total)`
)

func createSyntheticEndpoint(ctx context.Context, env *harness.Env, ns, tag string, i int) error {
	name := fmt.Sprintf("fn-bench-%s-%d", tag, i)
	fnLabel := fmt.Sprintf("bench-fn-%s-%d", tag, i)
	labels := map[string]string{
		benchSyntheticLabel:     tag,
		"fission.io/managed-by": "fission",
		fv1.FUNCTION_NAME:       fnLabel,
		fv1.FUNCTION_NAMESPACE:  ns,
	}
	svc := &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: apiv1.ServiceSpec{
			ClusterIP: apiv1.ClusterIPNone,
			Selector:  map[string]string{fv1.FUNCTION_NAME: fnLabel},
			Ports:     []apiv1.ServicePort{{Port: 8888, TargetPort: intstr.FromInt(8888)}},
		},
	}
	if _, err := env.Clients.Kube.CoreV1().Services(ns).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		return err
	}

	sliceLabels := maps.Clone(labels)
	sliceLabels[discoveryv1.LabelServiceName] = name
	// Spread synthetic IPs across the full 10.0.0.0/8 (offset by 1 to avoid the
	// .0.0.0 network address); valid for up to ~16M endpoints.
	idx := i + 1
	ip := fmt.Sprintf("10.%d.%d.%d", (idx>>16)&0xff, (idx>>8)&0xff, idx&0xff)
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: name + "-1", Namespace: ns, Labels: sliceLabels},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports:       []discoveryv1.EndpointPort{{Port: new(int32(8888))}},
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{ip},
			Conditions: discoveryv1.EndpointConditions{Ready: new(true)},
		}},
	}
	_, err := env.Clients.Kube.DiscoveryV1().EndpointSlices(ns).Create(ctx, slice, metav1.CreateOptions{})
	return err
}

func deleteSynthetic(ctx context.Context, env *harness.Env, ns, tag string) error {
	selector := benchSyntheticLabel + "=" + tag
	// EndpointSlices support DeleteCollection; Services are deleted individually.
	if err := env.Clients.Kube.DiscoveryV1().EndpointSlices(ns).DeleteCollection(ctx,
		metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: selector}); err != nil {
		return err
	}
	svcs, err := env.Clients.Kube.CoreV1().Services(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return err
	}
	for i := range svcs.Items {
		_ = env.Clients.Kube.CoreV1().Services(ns).Delete(ctx, svcs.Items[i].Name, metav1.DeleteOptions{})
	}
	return nil
}
