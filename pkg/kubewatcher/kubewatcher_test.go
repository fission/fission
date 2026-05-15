/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package kubewatcher

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// captureWatchedNamespace records the namespace passed to the Watch verb on
// the fake client. createKubernetesWatch hits client-go's Pods(ns).Watch path;
// the fake client exposes the request via WatchAction.Namespace.
func captureWatchedNamespace(t *testing.T, w *fv1.KubernetesWatchTrigger) (string, error) {
	t.Helper()
	kc := fake.NewSimpleClientset()
	var ns string
	kc.PrependWatchReactor("pods", func(action clienttesting.Action) (bool, watch.Interface, error) {
		ns = action.GetNamespace()
		return true, watch.NewFake(), nil
	})
	_, err := createKubernetesWatch(context.Background(), kc, w, "")
	return ns, err
}

func TestCreateKubernetesWatch_RejectsCrossNamespace(t *testing.T) {
	w := &fv1.KubernetesWatchTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "kwt-1", Namespace: "ns-attacker"},
		Spec: fv1.KubernetesWatchTriggerSpec{
			Namespace: "ns-victim",
			Type:      "POD",
		},
	}
	kc := fake.NewSimpleClientset()
	called := false
	kc.PrependWatchReactor("pods", func(action clienttesting.Action) (bool, watch.Interface, error) {
		called = true
		return true, watch.NewFake(), nil
	})
	_, err := createKubernetesWatch(context.Background(), kc, w, "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cross-namespace watch is not allowed") {
		t.Fatalf("expected cross-namespace error, got: %v", err)
	}
	if called {
		t.Fatalf("Watch reactor must not have been invoked when cross-namespace is rejected")
	}
}

func TestCreateKubernetesWatch_EmptySpecNamespaceCoercedToTriggerNamespace(t *testing.T) {
	w := &fv1.KubernetesWatchTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "kwt-1", Namespace: "ns-attacker"},
		Spec: fv1.KubernetesWatchTriggerSpec{
			Namespace: "",
			Type:      "POD",
		},
	}
	ns, err := captureWatchedNamespace(t, w)
	if err != nil {
		t.Fatalf("expected acceptance, got: %v", err)
	}
	// Pre-fix behaviour would have used "" → all namespaces. Post-fix must
	// resolve to the trigger's own namespace.
	if ns != "ns-attacker" {
		t.Fatalf("expected coerced namespace %q, got %q", "ns-attacker", ns)
	}
}

func TestCreateKubernetesWatch_SameNamespaceAccepted(t *testing.T) {
	w := &fv1.KubernetesWatchTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "kwt-1", Namespace: "default"},
		Spec: fv1.KubernetesWatchTriggerSpec{
			Namespace: "default",
			Type:      "POD",
		},
	}
	ns, err := captureWatchedNamespace(t, w)
	if err != nil {
		t.Fatalf("expected acceptance, got: %v", err)
	}
	if ns != "default" {
		t.Fatalf("expected namespace %q, got %q", "default", ns)
	}
}
