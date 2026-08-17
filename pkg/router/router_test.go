// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
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
	// Runs under testing/synctest so the 2 × 2s SAR retry delays per step
	// elapse instantly under virtual time. The test drives a genuine API
	// error (assert.AnError from the reactor), NOT a cancelled context —
	// syncInformers now distinguishes context.Canceled from a real API
	// failure and skips the former, so the cancellation path can no longer
	// stand in for a flake.
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

	synctest.Test(t, func(t *testing.T) {
		fakeClient := fake.NewClientset(fakeEndpointSlice)

		permissionCheck := atomic.Bool{}
		permissionCheck.Store(true)
		fakeClient.PrependReactor(
			"create",
			"selfsubjectaccessreviews",
			func(action k8stesting.Action) (bool, runtime.Object, error) {
				// SAFETY: this reactor is registered for "create" on selfsubjectaccessreviews, so the action is a CreateAction carrying a *SelfSubjectAccessReview.
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

		nsResolver := &utils.NamespaceResolver{}
		dynCache := &dynamicEndpointCache{
			kubeClient:  fakeClient,
			resolver:    nsResolver,
			nsInformers: nsManager,
			logger:      logr.Discard(),
		}
		nsResolver.AddTenant("team-a")

		for _, step := range steps {
			permissionCheck.Store(step.sarError)

			// syncInformers blocks on SAR retry delays (time.After inside
			// sliceWatchSAR) and on informer start/stop (nsInformers.Sync
			// waits on <-ni.done). Under synctest those are fake timers,
			// so run in a goroutine and advance virtual time.
			var got bool
			done := make(chan struct{})
			go func() {
				got = dynCache.syncInformers(t.Context())
				close(done)
			}()
			synctest.Wait()
			<-done

			// The informer's internal goroutines (reflector + processor) use
			// sync.Mutex/sync.Cond which are NOT durably blocking, so a single
			// Wait may return before the handler has processed the initial LIST.
			// Wait again so the processor goroutine gets scheduled and drains
			// its queue.
			synctest.Wait()

			assert.Equal(t, step.wantPending, got, step.name)
			// After synctest.Wait the informer goroutines have completed
			// their initial LIST (synchronous against the fake clientset)
			// and processed all queued handler calls, so the index size
			// is settled — no EventuallyWithT polling needed.
			assert.Equal(t, step.wantIndexSize, index.Size(), step.name)
			count, ok := dynCache.sarErrors["team-a"]
			assert.Equal(t, step.wantErrorEntry, ok, step.name)
			if step.wantErrorEntry {
				assert.Equal(t, step.wantErrorCount, count, step.name)
			}
			_, ok = dynCache.rbacChecked.Load("team-a")
			assert.Equal(t, step.wantPermissionCached, ok, step.name)
		}

		// Drain informer goroutines before the bubble closes.
		go func() { nsManager.Close() }()
		synctest.Wait()
	})
}
