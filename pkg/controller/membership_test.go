// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
)

func TestMembershipPredicate(t *testing.T) {
	r := &utils.NamespaceResolver{}
	r.SetTenants(map[string]string{"team-a": "team-a"})
	p := MembershipPredicate(r)

	inTenant := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a"}}
	notTenant := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Namespace: "team-b"}}

	assert.True(t, p.Create(event.CreateEvent{Object: inTenant}), "tenant-namespace object is admitted")
	assert.False(t, p.Create(event.CreateEvent{Object: notTenant}), "non-tenant object is dropped")
	assert.True(t, p.Delete(event.DeleteEvent{Object: inTenant}), "tenant-namespace delete is admitted")
	assert.False(t, p.Update(event.UpdateEvent{ObjectNew: notTenant}), "non-tenant update is dropped")

	// The predicate reads the live set, so a namespace onboarded at runtime
	// starts admitting without rebuilding the controller.
	r.AddTenant("team-b")
	assert.True(t, p.Create(event.CreateEvent{Object: notTenant}), "predicate must observe a later AddTenant")
}
