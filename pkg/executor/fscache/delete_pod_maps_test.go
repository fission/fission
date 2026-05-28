// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fscache

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// TestDeleteFunctionSvcCleansPodMaps guards against the leak where PodToFsvc
// and WebsocketFsvc (keyed by pod name) accumulated one entry per specialized
// pod and were never removed. DeleteFunctionSvc, on the path of both poolmgr
// cleanup routes, must drop those entries.
func TestDeleteFunctionSvcCleansPodMaps(t *testing.T) {
	fsc := MakeFunctionServiceCache(loggerfactory.GetLogger())
	require.NotNil(t, fsc)

	fsvc := &FuncSvc{
		Name:     "poolmgr-env-default-1-abcde",
		Function: &metav1.ObjectMeta{Name: "fn", Namespace: "default", UID: "uid-1"},
		Address:  "10.0.0.1",
	}

	fsc.PodToFsvc.Store(fsvc.Name, fsvc)
	fsc.WebsocketFsvc.Store(fsvc.Name, true)

	fsc.DeleteFunctionSvc(t.Context(), fsvc)

	_, ok := fsc.PodToFsvc.Load(fsvc.Name)
	assert.False(t, ok, "PodToFsvc entry should be removed after DeleteFunctionSvc")
	_, ok = fsc.WebsocketFsvc.Load(fsvc.Name)
	assert.False(t, ok, "WebsocketFsvc entry should be removed after DeleteFunctionSvc")
}
