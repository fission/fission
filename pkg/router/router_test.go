// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/endpointcache"
	"github.com/fission/fission/pkg/utils"
)

func TestDynamicCachePersistentSARErrorsEventuallyExcludeAndRecover(t *testing.T) {
	type testStep struct {
		name                 string
		sarError             bool
		wantPending          bool
		wantErrorEntry       bool
		wantIndexSize        int
		wantErrorCount       int
		wantPermissionCached bool
	}
	steps := []testStep{
		{
			name:                 "first permission-check error",
			sarError:             true,
			wantPending:          true,
			wantErrorEntry:       true,
			wantIndexSize:        1,
			wantErrorCount:       1,
			wantPermissionCached: false,
		},
		{
			name:                 "second permission-check error",
			sarError:             true,
			wantPending:          true,
			wantErrorEntry:       true,
			wantIndexSize:        1,
			wantErrorCount:       2,
			wantPermissionCached: false,
		},
		{
			name:                 "third permission-check error",
			sarError:             true,
			wantPending:          true,
			wantErrorEntry:       true,
			wantIndexSize:        1,
			wantErrorCount:       3,
			wantPermissionCached: false,
		},
		{
			name:                 "fourth permission-check error",
			sarError:             true,
			wantPending:          true,
			wantErrorEntry:       true,
			wantIndexSize:        0,
			wantErrorCount:       4,
			wantPermissionCached: false,
		},
		{
			name:                 "fifth permission-check error",
			sarError:             true,
			wantPending:          true,
			wantErrorEntry:       true,
			wantIndexSize:        0,
			wantErrorCount:       5,
			wantPermissionCached: false,
		},
		{
			name:                 "sixth permission-check allowed",
			sarError:             false,
			wantPending:          false,
			wantErrorEntry:       false,
			wantIndexSize:        1,
			wantErrorCount:       0,
			wantPermissionCached: true,
		},
	}
	port := int32(8888)
	ready := true
	fakeEndpointSlice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "testendpoint",
			Namespace: "team-a",
			Labels: map[string]string{
				fv1.FUNCTION_NAMESPACE: "team-a",
				fv1.MANAGED_BY_LABEL:   fv1.MANAGED_BY_VALUE,
				fv1.FUNCTION_NAME:      "fn-a",
			},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses: []string{"10.0.0.1"},
				Conditions: discoveryv1.EndpointConditions{
					Ready: &ready,
				},
			},
		},
		Ports: []discoveryv1.EndpointPort{
			{
				Port: &port,
			},
		},
	}

	fakeClient := fake.NewClientset(fakeEndpointSlice)

	permissionCheck := atomic.Bool{}
	permissionCheck.Store(true)
	fakeClient.PrependReactor(
		"create",
		"selfsubjectaccessreviews",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			sar := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
			if permissionCheck.Load() {
				return true, nil, assert.AnError
			}
			return true, &authorizationv1.SelfSubjectAccessReview{
				Spec: sar.Spec,
				Status: authorizationv1.SubjectAccessReviewStatus{
					Allowed: true,
				},
			}, nil
		},
	)

	index := endpointcache.NewIndex()
	nsManager := endpointcache.NewNamespaceInformers(
		fakeClient,
		index,
		logr.Discard(),
	)
	t.Cleanup(nsManager.Close)

	nsResolver := &utils.NamespaceResolver{}
	dynCache := &dynamicEndpointCache{
		kubeClient:  fakeClient,
		resolver:    nsResolver,
		nsInformers: nsManager,
		logger:      logr.Discard(),
	}
	nsResolver.AddTenant("team-a")

	errCtx, cancel := context.WithCancel(t.Context())
	cancel()

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			ctx := t.Context()
			if step.sarError {
				ctx = errCtx
			}
			permissionCheck.Store(step.sarError)
			got := dynCache.syncInformers(ctx)
			assert.Equal(t, step.wantPending, got)
			if step.wantIndexSize > 0 {
				require.True(t, nsManager.WaitForCacheSync(t.Context()))
			}

			require.EventuallyWithT(t, func(c *assert.CollectT) {
				assert.Equal(c, step.wantIndexSize, index.Size())
			}, 1*time.Second, 100*time.Millisecond,
			)
			count, ok := dynCache.sarErrors["team-a"]
			assert.Equal(t, step.wantErrorEntry, ok)
			if step.wantErrorEntry {
				assert.Equal(t, step.wantErrorCount, count)
			}
			_, ok = dynCache.rbacChecked.Load("team-a")
			assert.Equal(t, step.wantPermissionCached, ok)
		})
	}

}
