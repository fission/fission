// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"context"
	"errors"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

const testDeployUID = "deploy-uid-123"

// fnStore is an in-memory stand-in for the cluster used to exercise the generic
// reconciler without a fake clientset: the resourceOps closures read and mutate
// it directly.
type fnStore struct {
	objs map[string]fv1.Function
}

func newFnStore(items ...fv1.Function) *fnStore {
	s := &fnStore{objs: map[string]fv1.Function{}}
	for _, it := range items {
		s.objs[k8sCache.MetaObjectToName(&it.ObjectMeta).String()] = it
	}
	return s
}

func (s *fnStore) ops() resourceOps[fv1.Function, *fv1.Function] {
	return resourceOps[fv1.Function, *fv1.Function]{
		items: func(fr *FissionResources) []fv1.Function { return fr.Functions },
		list: func(_ context.Context) ([]fv1.Function, error) {
			out := make([]fv1.Function, 0, len(s.objs))
			for _, v := range s.objs {
				out = append(out, v)
			}
			return out, nil
		},
		meta:  func(f *fv1.Function) *metav1.ObjectMeta { return &f.ObjectMeta },
		equal: func(e, d *fv1.Function) bool { return reflect.DeepEqual(e.Spec, d.Spec) },
		create: func(_ context.Context, f *fv1.Function) (*metav1.ObjectMeta, error) {
			s.objs[k8sCache.MetaObjectToName(&f.ObjectMeta).String()] = *f
			return &f.ObjectMeta, nil
		},
		update: func(_ context.Context, _, d *fv1.Function) (*metav1.ObjectMeta, error) {
			s.objs[k8sCache.MetaObjectToName(&d.ObjectMeta).String()] = *d
			return &d.ObjectMeta, nil
		},
		delete: func(_ context.Context, ns, name string) error {
			delete(s.objs, k8sCache.MetaObjectToName(&metav1.ObjectMeta{Namespace: ns, Name: name}).String())
			return nil
		},
	}
}

func fn(name, env string, owned bool) fv1.Function {
	f := fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       fv1.FunctionSpec{Environment: fv1.EnvironmentReference{Name: env}},
	}
	if owned {
		f.Annotations = map[string]string{FISSION_DEPLOYMENT_UID_KEY: testDeployUID}
	}
	return f
}

func frWith(fns ...fv1.Function) *FissionResources {
	fr := &FissionResources{Functions: fns}
	fr.DeploymentConfig.UID = testDeployUID
	return fr
}

func TestApplyResourceType(t *testing.T) {
	ctx := context.Background()

	t.Run("create when absent", func(t *testing.T) {
		store := newFnStore()
		fr := frWith(fn("a", "nodejs", false))
		_, ras, err := applyResourceType(ctx, fr, store.ops(), false, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(ras.Created) != 1 || len(ras.Updated) != 0 || len(ras.Deleted) != 0 {
			t.Fatalf("expected 1 created, got C=%d U=%d D=%d", len(ras.Created), len(ras.Updated), len(ras.Deleted))
		}
		if _, ok := store.objs["default/a"]; !ok {
			t.Fatal("object not stored")
		}
	})

	t.Run("no-op when identical", func(t *testing.T) {
		store := newFnStore(fn("a", "nodejs", true))
		fr := frWith(fn("a", "nodejs", false))
		_, ras, err := applyResourceType(ctx, fr, store.ops(), false, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(ras.Created)+len(ras.Updated)+len(ras.Deleted) != 0 {
			t.Fatalf("expected no changes, got C=%d U=%d D=%d", len(ras.Created), len(ras.Updated), len(ras.Deleted))
		}
	})

	t.Run("update when spec differs", func(t *testing.T) {
		store := newFnStore(fn("a", "nodejs", true))
		fr := frWith(fn("a", "python", false)) // env changed
		_, ras, err := applyResourceType(ctx, fr, store.ops(), false, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(ras.Updated) != 1 || len(ras.Created) != 0 {
			t.Fatalf("expected 1 updated, got C=%d U=%d", len(ras.Created), len(ras.Updated))
		}
		if store.objs["default/a"].Spec.Environment.Name != "python" {
			t.Fatalf("update not persisted: %+v", store.objs["default/a"].Spec.Environment)
		}
	})

	t.Run("prune stale owned object when deleteStale", func(t *testing.T) {
		store := newFnStore(fn("stale", "nodejs", true), fn("keep", "nodejs", true))
		fr := frWith(fn("keep", "nodejs", false))
		_, ras, err := applyResourceType(ctx, fr, store.ops(), true, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(ras.Deleted) != 1 {
			t.Fatalf("expected 1 deleted, got %d", len(ras.Deleted))
		}
		if _, ok := store.objs["default/stale"]; ok {
			t.Fatal("stale object was not pruned")
		}
		if _, ok := store.objs["default/keep"]; !ok {
			t.Fatal("kept object was wrongly removed")
		}
	})

	t.Run("unowned objects are ignored unless conflicts allowed", func(t *testing.T) {
		// An object on the cluster without our deployment UID must not be pruned.
		store := newFnStore(fn("foreign", "nodejs", false))
		fr := frWith() // spec is empty
		_, ras, err := applyResourceType(ctx, fr, store.ops(), true, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(ras.Deleted) != 0 {
			t.Fatalf("foreign object should not be deleted, got %d deletes", len(ras.Deleted))
		}
		if _, ok := store.objs["default/foreign"]; !ok {
			t.Fatal("foreign object should remain")
		}
	})
}

func TestApplyResourceTypeStampsDeploymentUID(t *testing.T) {
	store := newFnStore()
	fr := frWith(fn("a", "nodejs", false))
	if _, _, err := applyResourceType(context.Background(), fr, store.ops(), false, false, false); err != nil {
		t.Fatal(err)
	}
	got := store.objs["default/a"]
	if got.Annotations[FISSION_DEPLOYMENT_UID_KEY] != testDeployUID {
		t.Fatalf("created object missing deployment UID annotation: %v", got.Annotations)
	}
}

func TestFilterByDeployID(t *testing.T) {
	items := []fv1.Function{fn("a", "nodejs", true), fn("b", "nodejs", false), fn("c", "nodejs", true)}
	got := filterByDeployID[fv1.Function](items, testDeployUID)
	if len(got) != 2 {
		t.Fatalf("expected 2 owned, got %d", len(got))
	}
	for _, f := range got {
		if f.Name == "b" {
			t.Fatal("unowned function b should have been filtered out")
		}
	}
}

func TestOwnedByDeployment(t *testing.T) {
	fr := frWith()
	owned := fn("a", "nodejs", true)
	foreign := fn("b", "nodejs", false)
	if !ownedByDeployment(&owned.ObjectMeta, fr) {
		t.Fatal("expected owned function to be recognised")
	}
	if ownedByDeployment(&foreign.ObjectMeta, fr) {
		t.Fatal("foreign function must not be recognised as owned")
	}
}

// A spec with an empty deployment UID (e.g. a missing/invalid
// fission-deployment-config.yaml) must own nothing, so it can never delete or
// mutate unannotated cluster resources.
func TestOwnedByDeploymentEmptyUID(t *testing.T) {
	fr := &FissionResources{} // DeploymentConfig.UID == ""
	annotated := fn("a", "nodejs", true)
	unannotated := fn("b", "nodejs", false)
	if ownedByDeployment(&annotated.ObjectMeta, fr) {
		t.Fatal("nothing should be owned when the deployment UID is empty")
	}
	if ownedByDeployment(&unannotated.ObjectMeta, fr) {
		t.Fatal("unannotated object must not be owned when the deployment UID is empty")
	}
}

func TestApplyResourceTypeEmptyUIDDeletesNothing(t *testing.T) {
	// Cluster holds objects, the spec is empty and its deployment UID is empty;
	// even with deleteStale + !allowConflicts, nothing may be deleted.
	store := newFnStore(fn("foreign", "nodejs", true), fn("other", "nodejs", false))
	fr := &FissionResources{} // empty UID, empty spec
	_, ras, err := applyResourceType(context.Background(), fr, store.ops(), true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ras.Deleted) != 0 {
		t.Fatalf("empty-UID spec must not delete anything, got %d deletes", len(ras.Deleted))
	}
	if len(store.objs) != 2 {
		t.Fatalf("cluster objects must be untouched, have %d (want 2)", len(store.objs))
	}
}

func TestSetDeploymentUID(t *testing.T) {
	fr := frWith()
	fr.DeploymentConfig.Name = "demo"
	f := fn("a", "nodejs", false)
	setDeploymentUID(&f.ObjectMeta, fr)
	if f.Annotations[FISSION_DEPLOYMENT_UID_KEY] != testDeployUID {
		t.Fatalf("uid annotation not set: %v", f.Annotations)
	}
	if f.Annotations[FISSION_DEPLOYMENT_NAME_KEY] != "demo" {
		t.Fatalf("name annotation not set: %v", f.Annotations)
	}
}

// TestApplyResourceTypeDryRun verifies that dryRun reports the same actions a
// real run would (create/update/delete) and populates the cross-ref metadata,
// while making no changes to the store.
func TestApplyResourceTypeDryRun(t *testing.T) {
	ctx := context.Background()

	t.Run("create is previewed but not performed", func(t *testing.T) {
		store := newFnStore()
		fr := frWith(fn("a", "nodejs", false))
		meta, ras, err := applyResourceType(ctx, fr, store.ops(), false, false, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(ras.Created) != 1 {
			t.Fatalf("expected 1 would-be create, got %d", len(ras.Created))
		}
		if len(store.objs) != 0 {
			t.Fatalf("dry run must not create anything, store has %d", len(store.objs))
		}
		if _, ok := meta["default/a"]; !ok {
			t.Fatal("cross-ref metadata must be recorded for the would-be create")
		}
	})

	t.Run("update is previewed but not performed", func(t *testing.T) {
		store := newFnStore(fn("a", "nodejs", true))
		fr := frWith(fn("a", "python", false)) // env differs -> would update
		_, ras, err := applyResourceType(ctx, fr, store.ops(), false, false, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(ras.Updated) != 1 {
			t.Fatalf("expected 1 would-be update, got %d", len(ras.Updated))
		}
		if got := store.objs["default/a"].Spec.Environment.Name; got != "nodejs" {
			t.Fatalf("dry run must not mutate the stored object, env=%q", got)
		}
	})

	t.Run("no-op stays a no-op", func(t *testing.T) {
		store := newFnStore(fn("a", "nodejs", true))
		fr := frWith(fn("a", "nodejs", false)) // identical spec
		_, ras, err := applyResourceType(ctx, fr, store.ops(), false, false, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(ras.Created)+len(ras.Updated) != 0 {
			t.Fatalf("identical spec should be a no-op, got C=%d U=%d", len(ras.Created), len(ras.Updated))
		}
	})

	t.Run("prune is previewed but not performed", func(t *testing.T) {
		store := newFnStore(fn("stale", "nodejs", true), fn("keep", "nodejs", true))
		fr := frWith(fn("keep", "nodejs", false))
		_, ras, err := applyResourceType(ctx, fr, store.ops(), true, false, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(ras.Deleted) != 1 {
			t.Fatalf("expected 1 would-be delete, got %d", len(ras.Deleted))
		}
		if _, ok := store.objs["default/stale"]; !ok {
			t.Fatal("dry run must not delete anything")
		}
		if len(store.objs) != 2 {
			t.Fatalf("store must be untouched, has %d (want 2)", len(store.objs))
		}
	})
}

// TestApplyResourceTypeDryRunValidates verifies that the read-only validate hook
// still runs under dryRun, so a preview surfaces a conflict (e.g. an HTTPTrigger
// duplicate route) that a real apply would reject — for both would-be creates
// and would-be updates — without mutating the store.
func TestApplyResourceTypeDryRunValidates(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("duplicate route")

	rejectingOps := func(s *fnStore) resourceOps[fv1.Function, *fv1.Function] {
		ops := s.ops()
		ops.validate = func(context.Context, *fv1.Function) error { return wantErr }
		return ops
	}

	t.Run("create preview surfaces validation error", func(t *testing.T) {
		store := newFnStore()
		fr := frWith(fn("a", "nodejs", false))
		_, _, err := applyResourceType(ctx, fr, rejectingOps(store), false, false, true)
		if !errors.Is(err, wantErr) {
			t.Fatalf("expected validation error, got %v", err)
		}
		if len(store.objs) != 0 {
			t.Fatalf("failed validation must not create anything, store has %d", len(store.objs))
		}
	})

	t.Run("update preview surfaces validation error", func(t *testing.T) {
		store := newFnStore(fn("a", "nodejs", true))
		fr := frWith(fn("a", "python", false)) // env differs -> would update
		_, _, err := applyResourceType(ctx, fr, rejectingOps(store), false, false, true)
		if !errors.Is(err, wantErr) {
			t.Fatalf("expected validation error, got %v", err)
		}
		if got := store.objs["default/a"].Spec.Environment.Name; got != "nodejs" {
			t.Fatalf("failed validation must not mutate the stored object, env=%q", got)
		}
	})
}
