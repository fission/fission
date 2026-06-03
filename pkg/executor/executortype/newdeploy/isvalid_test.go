// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/utils"
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

// TestNewdeployIsValid covers the IsValid path after it moved off the standalone
// SharedInformerFactory listers onto the executor Manager's cache-backed client.
func TestNewdeployIsValid(t *testing.T) {
	svcRef := apiv1.ObjectReference{Kind: "service", Namespace: "default", Name: "fn-svc"}
	deplRef := apiv1.ObjectReference{Kind: "deployment", Namespace: "default", Name: "fn-dep"}

	newDeploy := func(objs ...client.Object) *NewDeploy {
		c := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(objs...).Build()
		return &NewDeploy{logger: logr.Discard(), crClient: c}
	}

	t.Run("valid when service and ready deployment both exist", func(t *testing.T) {
		d := newDeploy(svcObj("default", "fn-svc"), deplObj("default", "fn-dep", 1))
		assert.True(t, d.IsValid(t.Context(), fsvcWith(svcRef, deplRef)))
	})

	t.Run("invalid when the deployment has no available replicas", func(t *testing.T) {
		d := newDeploy(svcObj("default", "fn-svc"), deplObj("default", "fn-dep", 0))
		assert.False(t, d.IsValid(t.Context(), fsvcWith(svcRef, deplRef)))
	})

	t.Run("invalid when the service is missing from the cache", func(t *testing.T) {
		d := newDeploy(deplObj("default", "fn-dep", 1)) // no service
		assert.False(t, d.IsValid(t.Context(), fsvcWith(svcRef, deplRef)))
	})

	t.Run("invalid when the deployment is missing from the cache", func(t *testing.T) {
		d := newDeploy(svcObj("default", "fn-svc")) // no deployment
		assert.False(t, d.IsValid(t.Context(), fsvcWith(svcRef, deplRef)))
	})

	t.Run("invalid when there are no kubernetes objects", func(t *testing.T) {
		d := newDeploy()
		assert.False(t, d.IsValid(t.Context(), fsvcWith()))
	})
}

// TestNewdeployResourcesExist covers the drift-detection read used by the
// level-triggered reconcile: present when both backing objects exist, absent when
// either has drifted away.
func TestNewdeployResourcesExist(t *testing.T) {
	fn := fnOfType("fn", fv1.ExecutorTypeNewdeploy)
	fn.UID = "83c82da2-81e9-4ebd-867e-f383e65e603f"

	build := func(objs ...client.Object) *NewDeploy {
		c := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(objs...).Build()
		return &NewDeploy{logger: logr.Discard(), crClient: c, nsResolver: &utils.NamespaceResolver{}}
	}
	// Object name/namespace match what createOrGet* use.
	name := build().getObjName(fn)
	ns := (&utils.NamespaceResolver{}).GetFunctionNS(fn.Namespace)

	t.Run("present when both Deployment and Service exist", func(t *testing.T) {
		d := build(deplObj(ns, name, 1), svcObj(ns, name))
		exist, err := d.resourcesExist(t.Context(), fn)
		require.NoError(t, err)
		assert.True(t, exist)
	})

	t.Run("absent when the Deployment has drifted away", func(t *testing.T) {
		d := build(svcObj(ns, name)) // no deployment
		exist, err := d.resourcesExist(t.Context(), fn)
		require.NoError(t, err)
		assert.False(t, exist)
	})

	t.Run("absent when the Service has drifted away", func(t *testing.T) {
		d := build(deplObj(ns, name, 1)) // no service
		exist, err := d.resourcesExist(t.Context(), fn)
		require.NoError(t, err)
		assert.False(t, exist)
	})
}
