// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/fission/fission/pkg/executor/fscache"
)

func svcObj(ns, name string) *apiv1.Service {
	return &apiv1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}

func deplObj(ns, name string, available int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: available},
	}
}

func fsvcWith(objs ...apiv1.ObjectReference) *fscache.FuncSvc {
	return &fscache.FuncSvc{
		Function:          &metav1.ObjectMeta{Name: "fn", Namespace: "default"},
		Address:           "10.0.0.1",
		KubernetesObjects: objs,
	}
}

// TestContainerIsValid covers the IsValid path after it moved off the standalone
// SharedInformerFactory listers onto the executor Manager's cache-backed client.
func TestContainerIsValid(t *testing.T) {
	svcRef := apiv1.ObjectReference{Kind: "service", Namespace: "default", Name: "fn-svc"}
	deplRef := apiv1.ObjectReference{Kind: "deployment", Namespace: "default", Name: "fn-dep"}

	newContainer := func(objs ...client.Object) *Container {
		c := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(objs...).Build()
		return &Container{logger: logr.Discard(), crClient: c}
	}

	t.Run("valid when service and ready deployment both exist", func(t *testing.T) {
		c := newContainer(svcObj("default", "fn-svc"), deplObj("default", "fn-dep", 1))
		assert.True(t, c.IsValid(t.Context(), fsvcWith(svcRef, deplRef)))
	})

	t.Run("invalid when the deployment has no available replicas", func(t *testing.T) {
		c := newContainer(svcObj("default", "fn-svc"), deplObj("default", "fn-dep", 0))
		assert.False(t, c.IsValid(t.Context(), fsvcWith(svcRef, deplRef)))
	})

	t.Run("invalid when the service is missing from the cache", func(t *testing.T) {
		c := newContainer(deplObj("default", "fn-dep", 1)) // no service
		assert.False(t, c.IsValid(t.Context(), fsvcWith(svcRef, deplRef)))
	})

	t.Run("invalid when the deployment is missing from the cache", func(t *testing.T) {
		c := newContainer(svcObj("default", "fn-svc")) // no deployment
		assert.False(t, c.IsValid(t.Context(), fsvcWith(svcRef, deplRef)))
	})

	t.Run("invalid when there are no kubernetes objects", func(t *testing.T) {
		c := newContainer()
		assert.False(t, c.IsValid(t.Context(), fsvcWith()))
	})
}
