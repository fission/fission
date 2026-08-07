// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"context"
	"fmt"

	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	kedaClient "github.com/kedacore/keda/v2/pkg/generated/clientset/versioned"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
)

const (
	KedaScaledObject          = "ScaledObject"
	KedaTriggerAuthentication = "TriggerAuthentication"
)

// mqtKind mirrors mqtrigger.MqtKind, the Kind the mqt-keda scaler stamps into
// the OwnerReferences of every KEDA object it creates. Duplicated rather than
// imported because pkg/mqtrigger would pull the controller-runtime scaler
// machinery into the CLI binary.
const mqtKind = "MessageQueueTrigger"

// KedaDumper dumps the KEDA objects that fission's mqt-keda scaler manager
// creates for MessageQueueTriggers (see pkg/mqtrigger/scalermanager.go).
//
// Only objects owned by a MessageQueueTrigger are dumped. KEDA is a
// cluster-wide component that other workloads use too, and a support bundle
// should carry fission's own resources, not an unrelated team's ScaledObjects.
// The scaler sets no labels on what it creates, so ownership is the only
// selector available — which is why this cannot reuse the label-selector-based
// KubernetesObjectDumper.
type KedaDumper struct {
	client   kedaClient.Interface
	kedaType string
	// clientErr defers a client-construction failure to Dump, so one
	// unavailable client cannot abort the whole support bundle.
	clientErr error
}

func NewKedaDumper(client cmd.Client, kedaType string) Resource {
	kc, err := kedaClient.NewForConfig(client.RestConfig)
	return KedaDumper{client: kc, kedaType: kedaType, clientErr: err}
}

// ownedByMessageQueueTrigger reports whether obj was created by fission's
// mqt-keda scaler for a MessageQueueTrigger.
func ownedByMessageQueueTrigger(meta metav1.ObjectMeta) bool {
	for _, ref := range meta.OwnerReferences {
		if ref.Kind == mqtKind {
			return true
		}
	}
	return false
}

// kedaAbsent reports whether err means the KEDA CRDs are not installed, as
// opposed to a real failure. A cluster with no KEDA is the normal case for
// anyone not using mqt of type keda, so it is reported as a verbose note
// rather than a warning that would look like a problem in every bundle.
func kedaAbsent(err error) bool {
	return apimeta.IsNoMatchError(err) || apierrors.IsNotFound(err)
}

func (res KedaDumper) Dump(ctx context.Context, dumpDir string) {
	if res.clientErr != nil {
		console.Warn(fmt.Sprintf("Error creating keda client for %v: %v", res.kedaType, res.clientErr))
		return
	}

	switch res.kedaType {
	case KedaScaledObject:
		items, err := res.client.KedaV1alpha1().ScaledObjects(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			if kedaAbsent(err) {
				console.Verbose(2, "KEDA ScaledObjects not available in this cluster, skipping")
				return
			}
			console.Warn(fmt.Sprintf("Error getting %v list: %v", res.kedaType, err))
			return
		}

		for _, item := range items.Items {
			if !ownedByMessageQueueTrigger(item.ObjectMeta) {
				continue
			}
			f := getFileName(dumpDir, item.ObjectMeta)
			writeToFile(f, item)
		}

	case KedaTriggerAuthentication:
		items, err := res.client.KedaV1alpha1().TriggerAuthentications(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			if kedaAbsent(err) {
				console.Verbose(2, "KEDA TriggerAuthentications not available in this cluster, skipping")
				return
			}
			console.Warn(fmt.Sprintf("Error getting %v list: %v", res.kedaType, err))
			return
		}

		for _, item := range items.Items {
			if !ownedByMessageQueueTrigger(item.ObjectMeta) {
				continue
			}
			cleaned := triggerAuthClean(&item)
			f := getFileName(dumpDir, cleaned.ObjectMeta)
			writeToFile(f, cleaned)
		}

	default:
		console.Warn(fmt.Sprintf("Unknown keda type: %v", res.kedaType))
	}
}

// triggerAuthClean masks the one field of a TriggerAuthentication that holds a
// credential inline rather than by reference: the HashiCorp Vault token. Every
// other auth source in the spec (secretTargetRef, configMapTargetRef, env,
// azureKeyVault's clientSecret.valueFrom) names a Secret or ConfigMap instead
// of carrying its value, so those are safe to dump as-is.
//
// Fission's own scaler only ever sets secretTargetRef, but a support bundle is
// sent to a third party, and nothing stops an operator editing the object the
// scaler created. Masks with "-" to show the field was set, matching pkgClean.
func triggerAuthClean(ta *kedav1alpha1.TriggerAuthentication) *kedav1alpha1.TriggerAuthentication {
	if ta.Spec.HashiCorpVault == nil || ta.Spec.HashiCorpVault.Credential == nil ||
		ta.Spec.HashiCorpVault.Credential.Token == "" {
		return ta
	}
	// Copy before masking: the token lives behind a pointer shared with the
	// List result, so mutating in place would edit the caller's object too.
	out := ta.DeepCopy()
	out.Spec.HashiCorpVault.Credential.Token = "-"
	return out
}
