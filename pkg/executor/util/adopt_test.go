// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fakeFission "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

// TestAdoptFunctionsFiltersByExecutorType verifies the shared adopt body calls
// create only for functions of the requested executor type (so the newdeploy
// and container managers don't adopt each other's — or poolmgr's — functions).
func TestAdoptFunctionsFiltersByExecutorType(t *testing.T) {
	ctx := t.Context()
	client := fakeFission.NewSimpleClientset() // nolint:staticcheck

	// DefaultNSResolver().FissionResourceNS defaults to {"default"} with no env,
	// so create the functions there for AdoptFunctions to find them.
	mk := func(name string, et fv1.ExecutorType) {
		_, err := client.CoreV1().Functions(metav1.NamespaceDefault).Create(ctx, &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: metav1.NamespaceDefault, UID: types.UID(name)},
			Spec: fv1.FunctionSpec{
				InvokeStrategy: fv1.InvokeStrategy{
					ExecutionStrategy: fv1.ExecutionStrategy{ExecutorType: et},
				},
			},
		}, metav1.CreateOptions{})
		require.NoErrorf(t, err, "create function %s", name)
	}
	mk("nd1", fv1.ExecutorTypeNewdeploy)
	mk("nd2", fv1.ExecutorTypeNewdeploy)
	mk("ctr1", fv1.ExecutorTypeContainer)
	mk("pm1", fv1.ExecutorTypePoolmgr)

	var mu sync.Mutex
	created := map[string]bool{}
	create := func(_ context.Context, fn *fv1.Function) error {
		mu.Lock()
		defer mu.Unlock()
		created[fn.Name] = true
		return nil
	}

	AdoptFunctions(ctx, logr.Discard(), client, fv1.ExecutorTypeNewdeploy, create)

	require.Equal(t, map[string]bool{"nd1": true, "nd2": true}, created,
		"AdoptFunctions must call create only for functions of the requested executor type")
}

// TestAdoptFunctionsToleratesCreateErrors verifies a per-function create failure
// is contained (logged) and doesn't stop the other functions from being adopted.
func TestAdoptFunctionsToleratesCreateErrors(t *testing.T) {
	ctx := t.Context()
	client := fakeFission.NewSimpleClientset() // nolint:staticcheck

	for _, name := range []string{"a", "b", "c"} {
		_, err := client.CoreV1().Functions(metav1.NamespaceDefault).Create(ctx, &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: metav1.NamespaceDefault, UID: types.UID(name)},
			Spec: fv1.FunctionSpec{
				InvokeStrategy: fv1.InvokeStrategy{
					ExecutionStrategy: fv1.ExecutionStrategy{ExecutorType: fv1.ExecutorTypeNewdeploy},
				},
			},
		}, metav1.CreateOptions{})
		require.NoErrorf(t, err, "create function %s", name)
	}

	var mu sync.Mutex
	attempted := map[string]bool{}
	create := func(_ context.Context, fn *fv1.Function) error {
		mu.Lock()
		defer mu.Unlock()
		attempted[fn.Name] = true
		if fn.Name == "b" {
			return context.DeadlineExceeded // one function fails to adopt
		}
		return nil
	}

	AdoptFunctions(ctx, logr.Discard(), client, fv1.ExecutorTypeNewdeploy, create)

	require.Equal(t, map[string]bool{"a": true, "b": true, "c": true}, attempted,
		"a single create failure must not stop the other functions from being adopted")
}
