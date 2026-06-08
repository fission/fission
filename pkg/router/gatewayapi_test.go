// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func gatewayTrigger(name string, parentRefs ...fv1.GatewayParentRef) *fv1.HTTPTrigger {
	rc := &fv1.RouteConfig{
		Provider:  fv1.RouteProviderGateway,
		Path:      "/api",
		Hostnames: []string{"demo.example.com"},
	}
	if len(parentRefs) > 0 {
		rc.Gateway = &fv1.GatewayRouteConfig{ParentRefs: parentRefs}
	}
	return &fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       fv1.HTTPTriggerSpec{RelativeURL: "/" + name, Methods: []string{"GET"}, RouteConfig: rc},
	}
}

func TestGatewayProviderLifecycle(t *testing.T) {
	logger := loggerfactory.GetLogger()
	client := gatewayfake.NewClientset()
	p := newGatewayRouteProvider(logger, client, nil)

	trigger := gatewayTrigger("t1", fv1.GatewayParentRef{Name: "eg", Namespace: "envoy-gateway"})

	// Create.
	require.NoError(t, p.Reconcile(t.Context(), trigger))
	hr, err := client.GatewayV1().HTTPRoutes(p.namespace).Get(t.Context(), "t1", metav1.GetOptions{})
	require.NoError(t, err, "httproute must be created for a gateway trigger")
	require.Len(t, hr.Spec.ParentRefs, 1)
	assert.Equal(t, gwapiv1.ObjectName("eg"), hr.Spec.ParentRefs[0].Name)
	require.NotNil(t, hr.Spec.ParentRefs[0].Namespace)
	assert.Equal(t, gwapiv1.Namespace("envoy-gateway"), *hr.Spec.ParentRefs[0].Namespace)
	require.Len(t, hr.Spec.Hostnames, 1)
	assert.Equal(t, gwapiv1.Hostname("demo.example.com"), hr.Spec.Hostnames[0])

	// Idempotent update (path changes).
	trigger.Spec.RouteConfig.Path = "/v2"
	require.NoError(t, p.Reconcile(t.Context(), trigger))
	hr, err = client.GatewayV1().HTTPRoutes(p.namespace).Get(t.Context(), "t1", metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, hr.Spec.Rules, 1)
	require.Len(t, hr.Spec.Rules[0].Matches, 1)
	assert.Equal(t, "/v2", *hr.Spec.Rules[0].Matches[0].Path.Value)

	// Switch provider to ingress -> gateway provider deletes its HTTPRoute.
	trigger.Spec.RouteConfig.Provider = fv1.RouteProviderIngress
	require.NoError(t, p.Reconcile(t.Context(), trigger))
	_, err = client.GatewayV1().HTTPRoutes(p.namespace).Get(t.Context(), "t1", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "httproute must be removed when the trigger switches away from the gateway provider")
}

func TestGatewayProviderUsesDefaultParentRefs(t *testing.T) {
	logger := loggerfactory.GetLogger()
	client := gatewayfake.NewClientset()
	defaultRefs := []gwapiv1.ParentReference{{Name: "shared-gw"}}
	p := newGatewayRouteProvider(logger, client, defaultRefs)

	// Trigger lists no parentRefs of its own -> falls back to the default.
	require.NoError(t, p.Reconcile(t.Context(), gatewayTrigger("t2")))
	hr, err := client.GatewayV1().HTTPRoutes(p.namespace).Get(t.Context(), "t2", metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, hr.Spec.ParentRefs, 1)
	assert.Equal(t, gwapiv1.ObjectName("shared-gw"), hr.Spec.ParentRefs[0].Name)
}

func TestGatewayProviderNoParentRefsErrors(t *testing.T) {
	logger := loggerfactory.GetLogger()
	client := gatewayfake.NewClientset()
	p := newGatewayRouteProvider(logger, client, nil)

	// No trigger parentRefs and no default -> error, no object created.
	err := p.Reconcile(t.Context(), gatewayTrigger("t3"))
	require.Error(t, err)
	_, getErr := client.GatewayV1().HTTPRoutes(p.namespace).Get(t.Context(), "t3", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(getErr))
}

func TestParseDefaultParentRefs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw       string
		wantLen   int
		wantName  string
		wantNS    string
		wantErr   bool
		wantNoRef bool
	}{
		{raw: "", wantNoRef: true},
		{raw: "  ", wantNoRef: true},
		{raw: "eg", wantLen: 1, wantName: "eg"},
		{raw: "envoy-gateway/eg", wantLen: 1, wantName: "eg", wantNS: "envoy-gateway"},
		{raw: "/eg", wantErr: true},
		{raw: "ns/", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			refs, err := parseDefaultParentRefs(tc.raw)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.wantNoRef {
				assert.Empty(t, refs)
				return
			}
			require.Len(t, refs, tc.wantLen)
			assert.Equal(t, gwapiv1.ObjectName(tc.wantName), refs[0].Name)
			if tc.wantNS != "" {
				require.NotNil(t, refs[0].Namespace)
				assert.Equal(t, gwapiv1.Namespace(tc.wantNS), *refs[0].Namespace)
			} else {
				assert.Nil(t, refs[0].Namespace)
			}
		})
	}
}
