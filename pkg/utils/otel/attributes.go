package otel

import (
	"go.opentelemetry.io/otel/attribute"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

/* GetAttributesForFunction returns a set of attributes for a function. Attributes returned:
    function-name
    function-namespace
    environment-name
    environment-namespace

These attributes are tags that can be used to filter traces.
*/
func GetAttributesForFunction(fn *fv1.Function) []attribute.KeyValue {
	return []attribute.KeyValue{
		{Key: "function-name", Value: attribute.StringValue(fn.Name)},
		{Key: "function-namespace", Value: attribute.StringValue(fn.Namespace)},
		{Key: "environment-name", Value: attribute.StringValue(fn.Spec.Environment.Name)},
		{Key: "environment-namespace", Value: attribute.StringValue(fn.Spec.Environment.Namespace)},
	}
}
