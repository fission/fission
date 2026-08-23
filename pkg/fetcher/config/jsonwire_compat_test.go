// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"encoding/json/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fetcher"
)

// TestFunctionSpecializeRequestJSONWireCompat is a compat gate for the
// json/v2 migration — this emission is a durable cross-version contract:
// NewSpecializeRequest's fetcher.FunctionSpecializeRequest (config.go,
// ~line 252) is JSON-encoded and passed as a "-specialize-request" pod
// command-line argument to fetcher sidecars in long-lived pool pods — pool
// pods are NOT rolled on a control-plane upgrade, so the reader may be an OLD
// fetcher binary running a v1 encoding/json decoder long after the
// control-plane has moved to json/v2. If this fails after a migration
// commit, add compat options at the marshal site in config.go, do not update
// the fixture.
//
// Note: FunctionSpecializeRequest's own FetchReq/LoadReq fields, and
// FunctionLoadRequest's FunctionMetadata field, carry no json tag, so their
// JSON object keys are the exported Go field names, not lowerCamelCase.
func TestFunctionSpecializeRequestJSONWireCompat(t *testing.T) {
	t.Parallel()

	t.Run("fully populated", func(t *testing.T) {
		t.Parallel()

		ts := metav1.NewTime(time.Date(2026, 8, 22, 10, 30, 0, 0, time.UTC))
		req := fetcher.FunctionSpecializeRequest{
			FetchReq: fetcher.FunctionFetchRequest{
				FetchType:     fv1.FETCH_DEPLOYMENT,
				Package:       metav1.ObjectMeta{Namespace: "default", Name: "hello-pkg", ResourceVersion: "12345"},
				URL:           "http://storagesvc.fission/v1/archive?id=abc",
				StorageSvcUrl: "http://storagesvc.fission",
				Filename:      "deployarchive",
				Secrets: []fv1.SecretReference{
					{Namespace: "default", Name: "mysecret", MountPath: "creds"},
				},
				ConfigMaps: []fv1.ConfigMapReference{
					{Namespace: "default", Name: "myconfig"},
				},
				KeepArchive: true,
			},
			LoadReq: fetcher.FunctionLoadRequest{
				FilePath:     "/userfunc/deployarchive",
				FunctionName: "Handler",
				URL:          "/",
				FunctionMetadata: &metav1.ObjectMeta{
					Name:              "hello",
					Namespace:         "default",
					UID:               "abc-123-uid",
					ResourceVersion:   "999",
					Generation:        2,
					Labels:            map[string]string{"app": "hello"},
					Annotations:       map[string]string{"fission.io/foo": "bar"},
					CreationTimestamp: ts,
				},
				EnvVersion:    2,
				StateKeyspace: "hello-state",
			},
		}
		const want = `{"FetchReq":{"fetchType":1,"package":{"name":"hello-pkg","namespace":"default","resourceVersion":"12345"},"url":"http://storagesvc.fission/v1/archive?id=abc","storagesvcurl":"http://storagesvc.fission","filename":"deployarchive","secretList":[{"namespace":"default","name":"mysecret","mountPath":"creds"}],"configMapList":[{"namespace":"default","name":"myconfig"}],"keeparchive":true},"LoadReq":{"filepath":"/userfunc/deployarchive","functionName":"Handler","url":"/","FunctionMetadata":{"name":"hello","namespace":"default","uid":"abc-123-uid","resourceVersion":"999","generation":2,"creationTimestamp":"2026-08-22T10:30:00Z","labels":{"app":"hello"},"annotations":{"fission.io/foo":"bar"}},"envVersion":2,"stateKeyspace":"hello-state"}}`

		got, err := json.Marshal(req, specializeWireOpts)
		require.NoError(t, err)
		require.Equal(t, want, string(got))

		var back fetcher.FunctionSpecializeRequest
		require.NoError(t, json.Unmarshal(got, &back))
		assert.Equal(t, req.FetchReq.FetchType, back.FetchReq.FetchType)
		assert.Equal(t, req.FetchReq.Package.Name, back.FetchReq.Package.Name)
		assert.Equal(t, req.FetchReq.Package.Namespace, back.FetchReq.Package.Namespace)
		assert.Equal(t, req.FetchReq.URL, back.FetchReq.URL)
		assert.Equal(t, req.FetchReq.Secrets, back.FetchReq.Secrets)
		assert.Equal(t, req.FetchReq.ConfigMaps, back.FetchReq.ConfigMaps)
		assert.Equal(t, req.FetchReq.KeepArchive, back.FetchReq.KeepArchive)
		assert.Equal(t, req.LoadReq.FilePath, back.LoadReq.FilePath)
		assert.Equal(t, req.LoadReq.FunctionName, back.LoadReq.FunctionName)
		require.NotNil(t, back.LoadReq.FunctionMetadata)
		assert.Equal(t, req.LoadReq.FunctionMetadata.Name, back.LoadReq.FunctionMetadata.Name)
		assert.Equal(t, req.LoadReq.FunctionMetadata.Labels, back.LoadReq.FunctionMetadata.Labels)
		assert.Equal(t, req.LoadReq.FunctionMetadata.Annotations, back.LoadReq.FunctionMetadata.Annotations)
		assert.True(t, req.LoadReq.FunctionMetadata.CreationTimestamp.Equal(&back.LoadReq.FunctionMetadata.CreationTimestamp))
		assert.Equal(t, req.LoadReq.EnvVersion, back.LoadReq.EnvVersion)
		assert.Equal(t, req.LoadReq.StateKeyspace, back.LoadReq.StateKeyspace)
	})

	t.Run("zero-heavy", func(t *testing.T) {
		t.Parallel()

		// Zero value: nil Secrets/ConfigMaps slices (no omitempty on either
		// tag) emit as JSON null; a nil FunctionMetadata pointer (no
		// omitempty, no tag at all) emits as the literal null under the
		// Go-field-name key "FunctionMetadata"; StateKeyspace (omitempty) is
		// omitted entirely. json/v2's differing null/[]/omit defaults for
		// untagged and omitempty fields is exactly the risk this pins.
		req := fetcher.FunctionSpecializeRequest{}
		const want = `{"FetchReq":{"fetchType":0,"package":{},"url":"","storagesvcurl":"","filename":"","secretList":null,"configMapList":null,"keeparchive":false},"LoadReq":{"filepath":"","functionName":"","url":"","FunctionMetadata":null,"envVersion":0}}`

		got, err := json.Marshal(req, specializeWireOpts)
		require.NoError(t, err)
		require.Equal(t, want, string(got))

		var back fetcher.FunctionSpecializeRequest
		require.NoError(t, json.Unmarshal(got, &back))
		assert.Equal(t, req.FetchReq.FetchType, back.FetchReq.FetchType)
		assert.Nil(t, back.FetchReq.Secrets)
		assert.Nil(t, back.FetchReq.ConfigMaps)
		assert.False(t, back.FetchReq.KeepArchive)
		assert.Nil(t, back.LoadReq.FunctionMetadata)
		assert.Equal(t, req.LoadReq.EnvVersion, back.LoadReq.EnvVersion)
		assert.Equal(t, req.LoadReq.StateKeyspace, back.LoadReq.StateKeyspace)
	})
}
