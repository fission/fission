// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package endpointcache

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func slice(name, fnName, fnNamespace string, port int32, addrs ...string) *discoveryv1.EndpointSlice {
	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "fn-ns",
			Labels: map[string]string{
				fv1.FUNCTION_NAME:      fnName,
				fv1.FUNCTION_NAMESPACE: fnNamespace,
				fv1.MANAGED_BY_LABEL:   fv1.MANAGED_BY_VALUE,
			},
		},
		Ports: []discoveryv1.EndpointPort{{Port: &port}},
	}
	ready := true
	for i, a := range addrs {
		es.Endpoints = append(es.Endpoints, discoveryv1.Endpoint{
			Addresses:  []string{a},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			TargetRef:  &apiv1.ObjectReference{Kind: "Pod", UID: types.UID(fmt.Sprintf("pod-%s-%d", a, i))},
		})
	}
	return es
}

func addrs(eps []Endpoint) []string {
	out := make([]string, 0, len(eps))
	for _, ep := range eps {
		out = append(out, ep.Address)
	}
	return out
}

func TestIndexApplyAndDelete(t *testing.T) {
	t.Parallel()
	ix := NewIndex()

	t.Run("slice add populates the function entry", func(t *testing.T) {
		ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1"))
		assert.ElementsMatch(t, []string{"10.0.0.1:8888"}, addrs(ix.Lookup("default", "fn-a")))
		assert.Equal(t, 1, ix.ReadyCount("default", "fn-a"))
		assert.Equal(t, 1, ix.Size())
	})

	t.Run("multiple slices for one function merge", func(t *testing.T) {
		ix.ApplySlice(slice("s2", "fn-a", "default", 8888, "10.0.0.2", "10.0.0.3"))
		assert.ElementsMatch(t,
			[]string{"10.0.0.1:8888", "10.0.0.2:8888", "10.0.0.3:8888"},
			addrs(ix.Lookup("default", "fn-a")))
		assert.Equal(t, 1, ix.Size(), "one function, regardless of slice count")
	})

	t.Run("slice update replaces only its own endpoints", func(t *testing.T) {
		ix.ApplySlice(slice("s2", "fn-a", "default", 8888, "10.0.0.9"))
		assert.ElementsMatch(t,
			[]string{"10.0.0.1:8888", "10.0.0.9:8888"},
			addrs(ix.Lookup("default", "fn-a")))
	})

	t.Run("slice delete removes its endpoints; last slice drops the entry", func(t *testing.T) {
		ix.DeleteSlice(slice("s2", "fn-a", "default", 8888))
		assert.ElementsMatch(t, []string{"10.0.0.1:8888"}, addrs(ix.Lookup("default", "fn-a")))
		ix.DeleteSlice(slice("s1", "fn-a", "default", 8888))
		assert.Empty(t, ix.Lookup("default", "fn-a"))
		assert.Equal(t, 0, ix.Size())
	})
}

func TestIndexIgnoresNonFissionSlices(t *testing.T) {
	t.Parallel()
	ix := NewIndex()
	es := slice("s1", "fn-a", "default", 8888, "10.0.0.1")
	es.Labels = map[string]string{} // no function labels
	ix.ApplySlice(es)
	assert.Equal(t, 0, ix.Size())
}

func TestIndexNotReadyEndpoints(t *testing.T) {
	t.Parallel()
	ix := NewIndex()
	es := slice("s1", "fn-a", "default", 8888, "10.0.0.1", "10.0.0.2")
	notReady := false
	es.Endpoints[1].Conditions.Ready = &notReady
	ix.ApplySlice(es)

	assert.Len(t, ix.Lookup("default", "fn-a"), 2, "not-ready endpoints stay visible (drain awareness)")
	assert.Equal(t, 1, ix.ReadyCount("default", "fn-a"))
}

func TestIndexNamespaceIsolation(t *testing.T) {
	t.Parallel()
	ix := NewIndex()
	ix.ApplySlice(slice("s1", "fn-a", "ns1", 8888, "10.0.0.1"))
	ix.ApplySlice(slice("s2", "fn-a", "ns2", 8888, "10.0.0.2"))
	assert.ElementsMatch(t, []string{"10.0.0.1:8888"}, addrs(ix.Lookup("ns1", "fn-a")))
	assert.ElementsMatch(t, []string{"10.0.0.2:8888"}, addrs(ix.Lookup("ns2", "fn-a")))
}

// TestIndexConcurrentReadersDuringEventStorm drives concurrent readers against
// a high-churn writer; the race detector is the assertion.
func TestIndexConcurrentReadersDuringEventStorm(t *testing.T) {
	t.Parallel()
	ix := NewIndex()
	const fns = 16

	var readers, writers sync.WaitGroup
	stop := make(chan struct{})
	for r := range 50 {
		readers.Go(func() {
			fn := fmt.Sprintf("fn-%d", r%fns)
			for {
				select {
				case <-stop:
					return
				default:
					_ = ix.Lookup("default", fn)
					_ = ix.ReadyCount("default", fn)
					_ = ix.Size()
				}
			}
		})
	}
	for w := range 8 {
		writers.Go(func() {
			for i := range 500 {
				fn := fmt.Sprintf("fn-%d", (w+i)%fns)
				s := slice(fmt.Sprintf("s-%d", w), fn, "default", 8888, fmt.Sprintf("10.0.%d.%d", w, i%250))
				if i%7 == 0 {
					ix.DeleteSlice(s)
				} else {
					ix.ApplySlice(s)
				}
			}
		})
	}
	writers.Wait()
	close(stop)
	readers.Wait()
	require.True(t, true, "the race detector is the real assertion")
}
