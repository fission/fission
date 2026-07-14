// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package asyncinvoke

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeliveryCondition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		res  DeliveryResult
		want string
	}{
		{"2xx", DeliveryResult{StatusCode: 200}, "2xx"},
		{"204", DeliveryResult{StatusCode: 204}, "2xx"},
		{"4xx", DeliveryResult{StatusCode: 404}, "4xx"},
		{"5xx", DeliveryResult{StatusCode: 503}, "5xx"},
		{"transport error", DeliveryResult{Err: errors.New("dial")}, "transport_error"},
		{"error beats status", DeliveryResult{StatusCode: 200, Err: errors.New("x")}, "transport_error"},
		{"3xx other", DeliveryResult{StatusCode: 302}, "other"},
		{"1xx other", DeliveryResult{StatusCode: 100}, "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, deliveryCondition(tc.res))
		})
	}
}

// TestRegisterQueueGaugesSmoke asserts the observable-gauge callbacks wire up
// against a real queue without panicking (the global no-op meter in tests makes
// this a wiring smoke test, not an emission assertion).
func TestRegisterQueueGaugesSmoke(t *testing.T) {
	q := memQueue(t)
	require.NotPanics(t, func() { RegisterQueueGauges(q, DefaultQueue) })
}
