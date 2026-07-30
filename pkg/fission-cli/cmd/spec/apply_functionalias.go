// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"context"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/util"
	typedv1 "github.com/fission/fission/pkg/generated/clientset/versioned/typed/core/v1"
)

// applyFunctionAliases reconciles FunctionAlias objects. Unlike the other
// apply* helpers this kind is mutable-by-controller: the version-control loop
// / alias-reconciler writes Status (ResolvedVersion, History, Conditions) via
// the /status subresource, so the update closure must change spec.* in place
// and leave Status untouched — never delete-recreate (a delete-recreate would
// lose the alias's UID, breaking anything that pinned it, and wipe the
// resolved-version history).
func applyFunctionAliases(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	aliases := func(ns string) typedv1.FunctionAliasInterface {
		return fclient.FissionClientSet.CoreV1().FunctionAliases(ns)
	}
	functions := func(ns string) typedv1.FunctionInterface {
		return fclient.FissionClientSet.CoreV1().Functions(ns)
	}
	return applyResourceType(ctx, fr, resourceOps[fv1.FunctionAlias, *fv1.FunctionAlias]{
		items: func(fr *FissionResources) []fv1.FunctionAlias {
			// Stamp the function-name label on every desired alias (mirrors
			// `fission alias create`, functionalias/create.go) so both sides
			// of the equality check in `equal` below see it, and it survives
			// update-in-place. Only the name label: it is deterministic from
			// spec.FunctionName and always known ahead of the target
			// Function existing, unlike the UID label FunctionVersion carries
			// (see const.go's VersionFunctionNameLabel doc).
			items := make([]fv1.FunctionAlias, len(fr.FunctionAliases))
			for i, a := range fr.FunctionAliases {
				if a.Labels == nil {
					a.Labels = make(map[string]string, 1)
				}
				a.Labels[fv1.VersionFunctionNameLabel] = a.Spec.FunctionName
				items[i] = a
			}
			return items
		},
		list: func(ctx context.Context) ([]fv1.FunctionAlias, error) {
			l, err := aliases(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return l.Items, nil
		},
		meta: func(a *fv1.FunctionAlias) *metav1.ObjectMeta { return &a.ObjectMeta },
		equal: func(e, d *fv1.FunctionAlias) bool {
			return isObjectMetaEqual(e.ObjectMeta, d.ObjectMeta) && reflect.DeepEqual(e.Spec, d.Spec)
		},
		create: func(ctx context.Context, a *fv1.FunctionAlias) (*metav1.ObjectMeta, error) {
			// Set an ownerRef to the target Function when it already exists on
			// the cluster, mirroring `fission alias create` (functionOwnerRef in
			// pkg/fission-cli/cmd/functionalias/create.go) so the alias is
			// garbage-collected along with its Function. The Function may not
			// exist yet for a digest-pinned alias applied ahead of it (eventual
			// consistency); the webhook does not require an ownerRef, so create
			// unowned in that case rather than failing the apply.
			if fn, err := functions(a.Namespace).Get(ctx, a.Spec.FunctionName, metav1.GetOptions{}); err == nil {
				a.OwnerReferences = []metav1.OwnerReference{fv1.FunctionOwnerRef(fn)}
			}
			n, err := aliases(a.Namespace).Create(ctx, a, metav1.CreateOptions{})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		update: func(ctx context.Context, _, d *fv1.FunctionAlias) (*metav1.ObjectMeta, error) {
			n, err := util.UpdateOnConflict(ctx, aliases(d.Namespace), d.Name, func(cur *fv1.FunctionAlias) {
				d.ResourceVersion = cur.ResourceVersion
				// Status lives on the /status subresource: a real apiserver
				// ignores status changes on this main-resource Update, but the
				// fake clientset used in tests does not enforce that split, so
				// preserve it explicitly rather than relying on server-side
				// behaviour. Likewise preserve OwnerReferences — the spec
				// object carries none (it's only computed in create, above),
				// so a naive overwrite would strip the ownerRef on every
				// subsequent apply. UID is server-owned and immutable on a
				// real cluster (an empty client-submitted UID is a no-op
				// there), but the fake clientset used in tests does not
				// enforce that either, so preserve it too — this is the
				// concrete proof that update is in place, not delete-recreate.
				d.Status = cur.Status
				d.OwnerReferences = cur.OwnerReferences
				d.UID = cur.UID
				*cur = *d
			})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		delete: func(ctx context.Context, ns, name string) error {
			return aliases(ns).Delete(ctx, name, metav1.DeleteOptions{})
		},
	}, delete, specAllowConflicts, dryRun)
}
