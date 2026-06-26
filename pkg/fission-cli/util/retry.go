// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

// updatableClient is the part of a typed clientset resource client that
// UpdateOnConflict needs. Every generated Fission/Kubernetes typed client
// (e.g. TimeTriggers(ns), Functions(ns)) satisfies it with T set to the
// resource pointer type.
type updatableClient[T any] interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (T, error)
	Update(ctx context.Context, obj T, opts metav1.UpdateOptions) (T, error)
}

// UpdateOnConflict re-fetches the named object, applies mutate to the fresh
// copy, and Updates it — retrying on optimistic-concurrency conflicts. Fission
// controllers write to an object's status concurrently with a
// `fission <resource> update`, bumping its resourceVersion and making a plain
// Get-modify-Update fail.
//
// mutate is invoked once per attempt on the freshly fetched object, so it must
// derive its change from the caller's inputs (e.g. cur.Spec = desiredSpec), not
// from a previously fetched copy. Because mutate re-applies the caller's desired
// state each attempt, a concurrent write to the same field is overwritten
// (last-write-wins); the common conflict — a controller status write racing a
// CLI spec write — touches a different field and is resolved cleanly.
func UpdateOnConflict[T any](ctx context.Context, c updatableClient[T], name string, mutate func(T)) (T, error) {
	var out T
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur, err := c.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		mutate(cur)
		out, err = c.Update(ctx, cur, metav1.UpdateOptions{})
		return err
	})
	return out, err
}
