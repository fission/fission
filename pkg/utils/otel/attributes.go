package otel

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	appsv1 "k8s.io/api/apps/v1"
	asv2 "k8s.io/api/autoscaling/v2"

	apiv1 "k8s.io/api/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

/*
	GetAttributesForFunction returns a set of attributes for a function. Attributes returned:
	   function-name
	   function-namespace
	   environment-name
	   environment-namespace

These attributes are tags that can be used to filter traces.
*/
func GetAttributesForFunction(fn *fv1.Function) []attribute.KeyValue {
	if fn == nil {
		return []attribute.KeyValue{}
	}
	attrs := []attribute.KeyValue{
		{Key: "function-name", Value: attribute.StringValue(fn.Name)},
		{Key: "function-namespace", Value: attribute.StringValue(fn.Namespace)},
	}
	if fn.Spec.Environment.Name != "" {
		attrs = append(attrs,
			attribute.KeyValue{Key: "environment-name", Value: attribute.StringValue(fn.Spec.Environment.Name)},
			attribute.KeyValue{Key: "environment-namespace", Value: attribute.StringValue(fn.Spec.Environment.Namespace)})
	}
	return attrs
}

func GetAttributesForEnv(env *fv1.Environment) []attribute.KeyValue {
	if env == nil {
		return []attribute.KeyValue{}
	}
	return []attribute.KeyValue{
		{Key: "environment-name", Value: attribute.StringValue(env.Name)},
		{Key: "environment-namespace", Value: attribute.StringValue(env.Namespace)}}
}

func GetAttributesForPackage(pkg *fv1.Package) []attribute.KeyValue {
	if pkg == nil {
		return []attribute.KeyValue{}
	}
	return []attribute.KeyValue{
		{Key: "package-name", Value: attribute.StringValue(pkg.Name)},
		{Key: "package-namespace", Value: attribute.StringValue(pkg.Namespace)}}
}

func GetAttributesForPod(pod *apiv1.Pod) []attribute.KeyValue {
	if pod == nil {
		return []attribute.KeyValue{}
	}
	return []attribute.KeyValue{
		{Key: "pod-name", Value: attribute.StringValue(pod.Name)},
		{Key: "pod-namespace", Value: attribute.StringValue(pod.Namespace)},
		{Key: "pod-ip", Value: attribute.StringValue(pod.Status.PodIP)},
	}
}

func GetAttributesForDeployment(deployment *appsv1.Deployment) []attribute.KeyValue {
	if deployment == nil {
		return []attribute.KeyValue{}
	}
	return []attribute.KeyValue{
		{Key: "deployment-name", Value: attribute.StringValue(deployment.Name)},
		{Key: "deployment-namespace", Value: attribute.StringValue(deployment.Namespace)},
	}
}

func GetAttributesForHPA(hpa *asv2.HorizontalPodAutoscaler) []attribute.KeyValue {
	if hpa == nil {
		return []attribute.KeyValue{}
	}
	return []attribute.KeyValue{
		{Key: "hpa-name", Value: attribute.StringValue(hpa.Name)},
		{Key: "hpa-namespace", Value: attribute.StringValue(hpa.Namespace)},
	}
}

func GetAttributesForSvc(svc *apiv1.Service) []attribute.KeyValue {
	if svc == nil {
		return []attribute.KeyValue{}
	}
	return []attribute.KeyValue{
		{Key: "svc-name", Value: attribute.StringValue(svc.Name)},
		{Key: "svc-namespace", Value: attribute.StringValue(svc.Namespace)},
	}
}

func MapToAttributes(m map[string]string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, len(m))
	for k, v := range m {
		attrs = append(attrs, attribute.KeyValue{Key: attribute.Key(k), Value: attribute.StringValue(v)})
	}
	return attrs
}

func SpanTrackEvent(ctx context.Context, event string, attributes ...attribute.KeyValue) {
	trace.SpanFromContext(ctx).AddEvent(event, trace.WithAttributes(attributes...))
}
