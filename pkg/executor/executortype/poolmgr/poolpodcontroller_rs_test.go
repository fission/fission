// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned/fake"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	fClient "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// TestProcessRSDiscriminator pins the keep-or-clean decision for a
// zero-replica pool ReplicaSet — the RFC-0028 mechanism-B fix. A pool rolls
// for exactly two reasons with OPPOSITE correct outcomes: an environment
// change must recycle the specialized pods born from the old template, and an
// executor-side template change (fetcher image on upgrade) must NOT — killing
// them there is the warm-pod death that made upgrades drop poolmgr requests.
func TestProcessRSDiscriminator(t *testing.T) {
	const (
		ns      = metav1.NamespaceDefault
		envName = "test-env"
		envUID  = "env-uid-1"
		depName = "poolmgr-test-env-default-123"
		depUID  = "dep-uid-1"
	)

	// Two concrete environments whose runtime hashes differ: the "live" one
	// and a prior revision (different runtime image). The discriminator and
	// the tests key on envRuntimeHash of these, never on synthetic strings —
	// so the test can only pass if the production hash function is what
	// distinguishes them.
	liveEnv := func() *fv1.Environment {
		return &fv1.Environment{
			ObjectMeta: metav1.ObjectMeta{Name: envName, Namespace: ns, UID: k8stypes.UID(envUID), Generation: 3},
			Spec:       fv1.EnvironmentSpec{Runtime: fv1.Runtime{Image: "img:v2"}},
		}
	}
	staleHash := envRuntimeHash(&fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: envName, Namespace: ns, UID: k8stypes.UID(envUID)},
		Spec:       fv1.EnvironmentSpec{Runtime: fv1.Runtime{Image: "img:v1"}},
	})
	liveHash := envRuntimeHash(liveEnv())

	templateLabels := func(hash string, overrides map[string]string) map[string]string {
		l := map[string]string{
			fv1.EXECUTOR_TYPE:         string(fv1.ExecutorTypePoolmgr),
			fv1.ENVIRONMENT_NAME:      envName,
			fv1.ENVIRONMENT_NAMESPACE: ns,
			fv1.ENVIRONMENT_UID:       envUID,
			"managed":                 "true",
			"pod-template-hash":       "abc123",
		}
		if hash != "" {
			l[fv1.ENVIRONMENT_RUNTIME_HASH] = hash
		}
		for k, v := range overrides {
			l[k] = v
		}
		return l
	}

	liveDep := func(deleting bool) *appsv1.Deployment {
		d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name: depName, Namespace: ns, UID: k8stypes.UID(depUID),
		}}
		if deleting {
			now := metav1.Now()
			d.DeletionTimestamp = &now
			// The fake API rejects a deletionTimestamp without a finalizer.
			d.Finalizers = []string{"test/hold"}
		}
		return d
	}
	// liveDepHashed is the owner AFTER the pool has been re-templated under a
	// hash-stamping release: its template carries the label.
	liveDepHashed := func() *appsv1.Deployment {
		d := liveDep(false)
		d.Spec.Template.Labels = map[string]string{fv1.ENVIRONMENT_RUNTIME_HASH: liveHash}
		return d
	}

	type tc struct {
		name string
		// rs shape
		replicas   int32
		ownerless  bool
		ownerUID   string
		tmplLabels map[string]string
		// cluster state
		dep         *appsv1.Deployment // direct kube client (owner Get)
		cachedEnv   *fv1.Environment   // manager-cache client
		uncachedEnv *fv1.Environment   // fission typed fake (stale-cache verify)
		depGetErr   bool               // inject a transient error on deployments get
		// expectations
		wantEnqueued int
		wantErr      bool
	}
	cases := []tc{
		{
			name:     "nonzero replicas is a no-op",
			replicas: 1, ownerUID: depUID, tmplLabels: templateLabels(liveHash, nil),
			dep: liveDep(false), cachedEnv: liveEnv(),
			wantEnqueued: 0,
		},
		{
			name:     "executor-side roll: live owner, runtime hash matches -> KEEP",
			ownerUID: depUID, tmplLabels: templateLabels(liveHash, nil),
			dep: liveDep(false), cachedEnv: liveEnv(),
			wantEnqueued: 0,
		},
		{
			name:     "environment runtime changed: hash moved -> clean",
			ownerUID: depUID, tmplLabels: templateLabels(staleHash, nil),
			dep: liveDep(false), cachedEnv: liveEnv(),
			wantEnqueued: 1,
		},
		{
			name:     "owner Deployment gone -> teardown -> clean",
			ownerUID: depUID, tmplLabels: templateLabels(liveHash, nil),
			cachedEnv:    liveEnv(),
			wantEnqueued: 1,
		},
		{
			name:     "owner Deployment deleting -> teardown -> clean",
			ownerUID: depUID, tmplLabels: templateLabels(liveHash, nil),
			dep: liveDep(true), cachedEnv: liveEnv(),
			wantEnqueued: 1,
		},
		{
			name:     "owner UID mismatch (name reused by a new Deployment) -> clean",
			ownerUID: "old-dep-uid", tmplLabels: templateLabels(liveHash, nil),
			dep: liveDep(false), cachedEnv: liveEnv(),
			wantEnqueued: 1,
		},
		{
			name:      "orphan RS (no controller ref) -> legacy clean",
			ownerless: true, tmplLabels: templateLabels(liveHash, nil),
			dep: liveDep(false), cachedEnv: liveEnv(),
			wantEnqueued: 1,
		},
		{
			name:     "env NotFound in cache but live in API -> stale cache -> requeue via error",
			ownerUID: depUID, tmplLabels: templateLabels(liveHash, nil),
			dep: liveDep(false), uncachedEnv: liveEnv(),
			// The RS reconciler filters resync events (GenerationChangedPredicate),
			// so a (false, nil) here would FORGET the decision forever; the error
			// is the only requeue mechanism. Nothing may be enqueued meanwhile.
			wantEnqueued: 0, wantErr: true,
		},
		{
			name:     "env gone from cache AND API -> teardown -> clean",
			ownerUID: depUID, tmplLabels: templateLabels(liveHash, nil),
			dep:          liveDep(false),
			wantEnqueued: 1,
		},
		{
			name:     "env recreated under the same name (UID mismatch) -> clean",
			ownerUID: depUID, tmplLabels: templateLabels(liveHash, map[string]string{fv1.ENVIRONMENT_UID: "stale-env-uid"}),
			dep: liveDep(false), cachedEnv: liveEnv(),
			wantEnqueued: 1,
		},
		{
			name:     "pre-label RS + pre-label owner -> KEEP — survival on the shipping upgrade",
			ownerUID: depUID, tmplLabels: templateLabels("", nil),
			dep: liveDep(false), cachedEnv: liveEnv(),
			wantEnqueued: 0,
		},
		{
			name:     "pre-label RS under a re-templated (hash-stamped) owner -> clean",
			ownerUID: depUID, tmplLabels: templateLabels("", nil),
			// The live pool Deployment's template already carries the label, so
			// this RS is provably a pre-hash generation: its pods run a runtime
			// at least one revision old. Without this branch they would be kept
			// FOREVER (a settled zero-replica RS emits no further events).
			dep: liveDepHashed(), cachedEnv: liveEnv(),
			wantEnqueued: 1,
		},
		{
			name:     "mangled hash label -> compares unequal -> clean",
			ownerUID: depUID, tmplLabels: templateLabels("not-a-real-hash", nil),
			dep: liveDep(false), cachedEnv: liveEnv(),
			wantEnqueued: 1,
		},
		{
			name:     "transient owner GET error -> error (requeue), nothing enqueued",
			ownerUID: depUID, tmplLabels: templateLabels(liveHash, nil),
			dep: liveDep(false), cachedEnv: liveEnv(), depGetErr: true,
			wantEnqueued: 0, wantErr: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logger := loggerfactory.GetLogger()

			// The specialized pod born from this template: matches the RS
			// selector with managed=false. Its presence is what makes
			// processRS reach the discriminator at all.
			specialized := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "specialized-1", Namespace: ns,
					Labels: func() map[string]string {
						l := templateLabels("", nil) // pod labels need no hash for selection
						l["managed"] = "false"
						return l
					}(),
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			}

			replicas := c.replicas
			rs := &appsv1.ReplicaSet{
				ObjectMeta: metav1.ObjectMeta{Name: depName + "-abc123", Namespace: ns},
				Spec: appsv1.ReplicaSetSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
						fv1.ENVIRONMENT_NAME: envName, "managed": "true", "pod-template-hash": "abc123",
					}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: c.tmplLabels},
					},
				},
			}
			if !c.ownerless {
				isController := true
				rs.OwnerReferences = []metav1.OwnerReference{{
					APIVersion: "apps/v1", Kind: "Deployment", Name: depName,
					UID: k8stypes.UID(c.ownerUID), Controller: &isController,
				}}
			}

			kubeObjs := []runtime.Object{}
			if c.dep != nil {
				kubeObjs = append(kubeObjs, c.dep)
			}
			kubernetesClient := fake.NewClientset(kubeObjs...)
			if c.depGetErr {
				kubernetesClient.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("transient apiserver error")
				})
			}

			s := runtime.NewScheme()
			require.NoError(t, clientgoscheme.AddToScheme(s))
			require.NoError(t, fv1.AddToScheme(s))
			crObjs := []crclient.Object{specialized}
			if c.cachedEnv != nil {
				crObjs = append(crObjs, c.cachedEnv)
			}
			crClient := crfake.NewClientBuilder().WithScheme(s).WithObjects(crObjs...).Build()

			var fissionClient *fClient.Clientset
			if c.uncachedEnv != nil {
				fissionClient = fClient.NewClientset(c.uncachedEnv)
			} else {
				fissionClient = fClient.NewClientset()
			}

			ppc := NewPoolPodController(logger, kubernetesClient)
			metricsClient := metricsclient.NewSimpleClientset() //nolint:staticcheck
			fetcherCfg, err := fetcherConfig.MakeFetcherConfig("/userfunc")
			require.NoError(t, err)
			executor, err := MakeGenericPoolManager(t.Context(), logger,
				fissionClient, kubernetesClient, metricsClient, fetcherCfg, "test-instance", nil)
			require.NoError(t, err)
			gpm := executor.(*GenericPoolManager)
			gpm.crClient = crClient
			ppc.InjectGpm(gpm)

			err = ppc.processRS(t.Context(), rs)
			if c.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, c.wantEnqueued, ppc.spCleanupPodQueue.Len(),
				"cleanup queue length after processRS")
		})
	}
}

// TestPoolSelectorStaysHashFree is the selector-immutability guard AND the
// alias-mutation guard: the ENVIRONMENT_RUNTIME_HASH label may exist ONLY on
// the pod template (a Deployment selector is immutable, so a hash-bearing
// selector would fail every environment update with "field is immutable") —
// and genDeploymentSpec must never write it back into the caller's
// Environment, whose labels are long-lived (gp.env) and feed Service
// selectors on every specialization.
func TestPoolSelectorStaysHashFree(t *testing.T) {
	env := &fv1.Environment{
		// Non-nil Labels, deliberately: with nil labels genDeploymentSpec
		// allocates a fresh map and the aliasing bug is invisible. A labelled
		// env is the case where writing pool/hash labels back into env.Labels
		// leaked them into Service selectors.
		ObjectMeta: metav1.ObjectMeta{Name: "e", Namespace: "default", UID: "u", Generation: 7,
			Labels: map[string]string{"team": "core"}},
		Spec: fv1.EnvironmentSpec{Version: 3, Poolsize: 3, Runtime: fv1.Runtime{Image: "img"}},
	}
	fetcherCfg, err := fetcherConfig.MakeFetcherConfig("/userfunc")
	require.NoError(t, err)
	gp := &GenericPool{fnNamespace: "default", fetcherConfig: fetcherCfg}

	spec, err := gp.genDeploymentSpec(env)
	require.NoError(t, err)

	assert.NotContains(t, spec.Selector.MatchLabels, fv1.ENVIRONMENT_RUNTIME_HASH,
		"the runtime hash must NEVER reach the immutable selector")
	assert.Equal(t, envRuntimeHash(env), spec.Template.Labels[fv1.ENVIRONMENT_RUNTIME_HASH],
		"the pod template must carry the runtime hash — it is what processRS keys on")
	assert.Equal(t, map[string]string{"team": "core"}, env.Labels,
		"genDeploymentSpec must not mutate the caller's Environment labels — gp.env is long-lived and feeds every specialization's label copy")
}

// TestEnvRuntimeHashIgnoresNonTemplateSpec pins the discriminator's key
// choice: spec fields that never reach the pod template — poolsize above all,
// a pure replica scale — must not move the hash. Keying on generation made
// `env update --poolsize` recycle every warm pod, under load, which is the
// exact operation the scale-up serves.
func TestEnvRuntimeHashIgnoresNonTemplateSpec(t *testing.T) {
	base := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "e", Namespace: "default", UID: "u", Generation: 3},
		Spec:       fv1.EnvironmentSpec{Version: 3, Poolsize: 3, Runtime: fv1.Runtime{Image: "img"}},
	}
	scaled := base.DeepCopy()
	scaled.Spec.Poolsize = 30
	scaled.Generation = 4 // any spec write moves generation; the hash must not care
	assert.Equal(t, envRuntimeHash(base), envRuntimeHash(scaled),
		"poolsize feeds only Replicas, never the template — scaling must not recycle warm pods")

	reimaged := base.DeepCopy()
	reimaged.Spec.Runtime.Image = "img:v2"
	assert.NotEqual(t, envRuntimeHash(base), envRuntimeHash(reimaged),
		"a runtime image change is exactly what the hash exists to catch")

	relabelled := base.DeepCopy()
	relabelled.Labels = map[string]string{"tier": "gold"}
	assert.NotEqual(t, envRuntimeHash(base), envRuntimeHash(relabelled),
		"env labels land in the pod template and must move the hash")

	// The hash is fed the EFFECTIVE grace, so nil (defaulted to 90 by the
	// apiserver) and an explicit 90 bake the same template and must hash
	// alike — this is also what kept the hash byte-stable across the
	// int64 -> *int64 field migration (no warm-pod recycle wave on the
	// release that shipped it).
	defaulted := base.DeepCopy() // TerminationGracePeriod nil
	explicit := base.DeepCopy()
	explicit.Spec.TerminationGracePeriod = new(fv1.DefaultTerminationGracePeriod)
	assert.Equal(t, envRuntimeHash(defaulted), envRuntimeHash(explicit),
		"nil and explicit-default grace produce the same template and must not differ in hash")

	instantKill := base.DeepCopy()
	instantKill.Spec.TerminationGracePeriod = new(int64(0))
	assert.NotEqual(t, envRuntimeHash(base), envRuntimeHash(instantKill),
		"an explicit 0 grace changes the template (no preStop drain) and must move the hash")
}
