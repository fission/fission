// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

// Route-shape derivation golden tests (RFC-0013 phase 0).
//
// These pin how an HTTPTrigger spec is derived into mux registrations TODAY,
// so the incremental route-table refactor (phases 1-2) has a behavioral
// contract that must pass unchanged. Every assertion is registration-level
// (muxMatches) rather than handler-level: shape is what the table owns;
// handlers are swappable by design.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bep/debounce"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// newShapeTS builds an HTTPTriggerSet whose resolver is backed by a fake
// cache client holding the given functions, so triggers that reference them
// resolve. This is the harness for shape tests that need real resolution
// (newTestTriggerSet's nil reader only supports trigger-less sets).
func newShapeTS(t testing.TB, functions []fv1.Function, triggers []fv1.HTTPTrigger) *HTTPTriggerSet {
	t.Helper()
	logger := loggerfactory.GetLogger()
	builder := fake.NewClientBuilder().WithScheme(scheme.Scheme)
	for i := range functions {
		builder = builder.WithObjects(&functions[i])
	}
	cl := builder.Build()
	ts := &HTTPTriggerSet{
		logger:                     logger.WithName("shape_test"),
		triggers:                   triggers,
		functions:                  functions,
		client:                     cl,
		updateRouterRequestChannel: make(chan struct{}, 1),
		syncDebouncer:              debounce.New(time.Millisecond),
		resolver:                   makeFunctionReferenceResolver(logger, cl),
	}
	return ts
}

func shapeFn(name string) fv1.Function {
	return fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
}

func shapeTrigger(name string, mutate func(*fv1.HTTPTrigger)) fv1.HTTPTrigger {
	tr := fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: fv1.HTTPTriggerSpec{
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "fn",
			},
			Methods: []string{http.MethodGet},
		},
	}
	if mutate != nil {
		mutate(&tr)
	}
	return tr
}

// TestRouteShapeExactPath pins the RelativeURL form: an exact gorilla route
// gated by the trigger's methods — no implicit prefix matching, no method
// widening.
func TestRouteShapeExactPath(t *testing.T) {
	ts := newShapeTS(t, []fv1.Function{shapeFn("fn")},
		[]fv1.HTTPTrigger{shapeTrigger("t", func(tr *fv1.HTTPTrigger) {
			tr.Spec.RelativeURL = "/hello"
		})})
	public, _, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	assert.True(t, muxMatches(public, http.MethodGet, "/hello"), "exact path+method must match")
	assert.False(t, muxMatches(public, http.MethodPost, "/hello"), "method outside the trigger's set must not match")
	assert.False(t, muxMatches(public, http.MethodGet, "/hello/sub"), "RelativeURL must not register a prefix")
	assert.False(t, muxMatches(public, http.MethodGet, "/helloworld"), "exact path must not match a longer sibling")
}

// TestRouteShapeSlashSuffixedPrefix pins the Prefix-ending-in-slash form: a
// single PathPrefix registration; the bare prefix (without the trailing
// slash) does NOT match — only the slash-suffixed subtree does.
func TestRouteShapeSlashSuffixedPrefix(t *testing.T) {
	ts := newShapeTS(t, []fv1.Function{shapeFn("fn")},
		[]fv1.HTTPTrigger{shapeTrigger("t", func(tr *fv1.HTTPTrigger) {
			tr.Spec.Prefix = new(string)
			*tr.Spec.Prefix = "/api/"
		})})
	public, _, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	assert.True(t, muxMatches(public, http.MethodGet, "/api/"), "the prefix root must match")
	assert.True(t, muxMatches(public, http.MethodGet, "/api/v1/users"), "subtree paths must match")
	assert.False(t, muxMatches(public, http.MethodGet, "/api"), "bare path without the trailing slash must not match a slash-suffixed prefix")
	assert.False(t, muxMatches(public, http.MethodGet, "/apifoo"), "sibling path must not match")
}

// TestRouteShapeDualRegistrationBoundary pins the non-slash Prefix form: TWO
// registrations (exact `/api` + PathPrefix `/api/`), which is exactly what
// prevents `/api` from matching `/apifoo`. This boundary is the route *pair*
// the RFC's consistency model calls out — both derive from one spec and must
// swap together.
func TestRouteShapeDualRegistrationBoundary(t *testing.T) {
	ts := newShapeTS(t, []fv1.Function{shapeFn("fn")},
		[]fv1.HTTPTrigger{shapeTrigger("t", func(tr *fv1.HTTPTrigger) {
			tr.Spec.Prefix = new(string)
			*tr.Spec.Prefix = "/api"
		})})
	public, _, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	assert.True(t, muxMatches(public, http.MethodGet, "/api"), "the exact registration must match the bare prefix")
	assert.True(t, muxMatches(public, http.MethodGet, "/api/"), "the prefix registration must match the slashed root")
	assert.True(t, muxMatches(public, http.MethodGet, "/api/v1/users"), "subtree paths must match")
	assert.False(t, muxMatches(public, http.MethodGet, "/apifoo"), "the dual registration must not glob beyond the path-segment boundary")
	assert.False(t, muxMatches(public, http.MethodPost, "/api"), "methods gate both registrations")
}

// TestRouteShapePrefixWinsOverRelativeURL pins precedence between the two
// spec fields: when both Prefix and RelativeURL are set, only the prefix is
// registered (the if/else in buildMuxes — RelativeURL is ignored).
func TestRouteShapePrefixWinsOverRelativeURL(t *testing.T) {
	ts := newShapeTS(t, []fv1.Function{shapeFn("fn")},
		[]fv1.HTTPTrigger{shapeTrigger("t", func(tr *fv1.HTTPTrigger) {
			tr.Spec.Prefix = new(string)
			*tr.Spec.Prefix = "/pfx/"
			tr.Spec.RelativeURL = "/rel"
		})})
	public, _, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	assert.True(t, muxMatches(public, http.MethodGet, "/pfx/x"), "prefix must be registered")
	assert.False(t, muxMatches(public, http.MethodGet, "/rel"), "RelativeURL must be ignored when Prefix is set")
}

// TestRouteShapeLegacyMethodMerged pins the singular Spec.Method legacy
// field: it is appended to Spec.Methods when absent and not duplicated when
// present.
func TestRouteShapeLegacyMethodMerged(t *testing.T) {
	ts := newShapeTS(t, []fv1.Function{shapeFn("fn")},
		[]fv1.HTTPTrigger{shapeTrigger("t", func(tr *fv1.HTTPTrigger) {
			tr.Spec.RelativeURL = "/m"
			tr.Spec.Methods = []string{http.MethodGet}
			tr.Spec.Method = http.MethodPost
		})})
	public, _, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	assert.True(t, muxMatches(public, http.MethodGet, "/m"))
	assert.True(t, muxMatches(public, http.MethodPost, "/m"), "legacy Method must be merged into the method set")
	assert.False(t, muxMatches(public, http.MethodDelete, "/m"))
}

// TestRouteShapeEmptyMethods pins the empty-method-set edge: a trigger with
// neither Methods nor Method registers a route whose method matcher is an
// empty allowlist — it matches NOTHING (a dead route), rather than matching
// every method. The CLI/CRD default ("GET") makes this rare, but the
// materializer must keep deriving the same dead shape rather than "fixing"
// it into a match-all.
func TestRouteShapeEmptyMethods(t *testing.T) {
	ts := newShapeTS(t, []fv1.Function{shapeFn("fn")},
		[]fv1.HTTPTrigger{shapeTrigger("t", func(tr *fv1.HTTPTrigger) {
			tr.Spec.RelativeURL = "/dead"
			tr.Spec.Methods = nil
		})})
	public, _, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodOptions} {
		assert.False(t, muxMatches(public, m, "/dead"),
			"an empty method set must match no method (%s)", m)
	}
}

// TestRouteShapeHostQualified pins Spec.Host: the route matches only when the
// request carries that literal host; a host-less sibling route on a different
// path is unaffected.
func TestRouteShapeHostQualified(t *testing.T) {
	ts := newShapeTS(t, []fv1.Function{shapeFn("fn")}, []fv1.HTTPTrigger{
		shapeTrigger("hosted", func(tr *fv1.HTTPTrigger) {
			tr.Spec.RelativeURL = "/hosted"
			tr.Spec.Host = "api.example.com"
		}),
	})
	public, _, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	reqMatches := func(host string) bool {
		req := httptest.NewRequest(http.MethodGet, "/hosted", nil)
		req.Host = host
		var match mux.RouteMatch
		return public.Match(req, &match) && match.Handler != nil
	}
	assert.True(t, reqMatches("api.example.com"), "matching host must route")
	assert.False(t, reqMatches("other.example.com"), "non-matching host must not route")
	assert.False(t, reqMatches(""), "absent host must not route a host-qualified trigger")
}

// TestRouteShapeCORSAppendsOptions pins the CORS preflight plumbing: a
// trigger with a CorsConfig gets OPTIONS appended to its registered methods
// so the preflight reaches the CORSAllowlist wrapper instead of gorilla's
// 405; a trigger without CorsConfig does NOT gain OPTIONS.
func TestRouteShapeCORSAppendsOptions(t *testing.T) {
	ts := newShapeTS(t, []fv1.Function{shapeFn("fn")}, []fv1.HTTPTrigger{
		shapeTrigger("cors", func(tr *fv1.HTTPTrigger) {
			tr.Spec.RelativeURL = "/cors"
			tr.Spec.CorsConfig = &fv1.HTTPTriggerCorsConfig{
				AllowOrigins: []string{"https://app.example.com"},
			}
		}),
		shapeTrigger("plain", func(tr *fv1.HTTPTrigger) {
			tr.Spec.RelativeURL = "/plain"
		}),
	})
	public, _, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	assert.True(t, muxMatches(public, http.MethodOptions, "/cors"),
		"a CORS trigger must register OPTIONS for the preflight")
	assert.False(t, muxMatches(public, http.MethodOptions, "/plain"),
		"a non-CORS trigger must not gain OPTIONS")
}

// TestRouteShapeGKEHomeFallback pins the router-owned "/" probe: registered
// (GET+OPTIONS, deny-all CORS) when no user trigger claims GET / exactly, and
// suppressed when one does.
func TestRouteShapeGKEHomeFallback(t *testing.T) {
	t.Run("no user route on /", func(t *testing.T) {
		ts := newShapeTS(t, []fv1.Function{shapeFn("fn")},
			[]fv1.HTTPTrigger{shapeTrigger("t", func(tr *fv1.HTTPTrigger) {
				tr.Spec.RelativeURL = "/elsewhere"
			})})
		public, _, err := ts.buildMuxes(t.Context(), nil)
		require.NoError(t, err)
		rr := httptest.NewRecorder()
		public.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		assert.Equal(t, http.StatusOK, rr.Code, "the GKE-ingress health fallback must answer GET / with 200")
	})
	t.Run("user trigger on GET / suppresses the fallback", func(t *testing.T) {
		ts := newShapeTS(t, []fv1.Function{shapeFn("fn")},
			[]fv1.HTTPTrigger{shapeTrigger("home", func(tr *fv1.HTTPTrigger) {
				tr.Spec.RelativeURL = "/"
				tr.Spec.Methods = []string{http.MethodGet}
			})})
		public, _, err := ts.buildMuxes(t.Context(), nil)
		require.NoError(t, err)
		// The user route owns "/": OPTIONS (which only the fallback would
		// register) must not match.
		assert.True(t, muxMatches(public, http.MethodGet, "/"))
		assert.False(t, muxMatches(public, http.MethodOptions, "/"),
			"the deny-all fallback must be suppressed when a user trigger claims GET /")
	})
}

// TestRouteShapeSkipsInvalidAndUnresolvable pins the per-trigger isolation
// property: a trigger with invalid CORS config and a trigger whose function
// does not exist are both skipped (404), while a healthy sibling trigger in
// the same build still serves.
func TestRouteShapeSkipsInvalidAndUnresolvable(t *testing.T) {
	ts := newShapeTS(t, []fv1.Function{shapeFn("fn")}, []fv1.HTTPTrigger{
		shapeTrigger("bad-cors", func(tr *fv1.HTTPTrigger) {
			tr.Spec.RelativeURL = "/bad-cors"
			tr.Spec.CorsConfig = &fv1.HTTPTriggerCorsConfig{
				AllowOrigins: []string{"https://app.example.com"},
				MaxAge:       "not-a-duration",
			}
		}),
		shapeTrigger("no-fn", func(tr *fv1.HTTPTrigger) {
			tr.Spec.RelativeURL = "/no-fn"
			tr.Spec.FunctionReference.Name = "ghost"
		}),
		shapeTrigger("healthy", func(tr *fv1.HTTPTrigger) {
			tr.Spec.RelativeURL = "/healthy"
		}),
	})
	public, _, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	assert.False(t, muxMatches(public, http.MethodGet, "/bad-cors"), "invalid CORS config must skip the route")
	assert.False(t, muxMatches(public, http.MethodGet, "/no-fn"), "unresolvable function must skip the route")
	assert.True(t, muxMatches(public, http.MethodGet, "/healthy"), "a healthy sibling must still be registered")
}

// TestRouteShapeInternalNamespaceFolding pins the internal mux's function
// routes: the default namespace folds to /fission-function/<name> (the form
// the publishers build via utils.UrlForFunction), non-default namespaces keep
// the /fission-function/<ns>/<name> form, and each function registers the
// exact route plus its slash-prefix subtree.
func TestRouteShapeInternalNamespaceFolding(t *testing.T) {
	fnDefault := fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fd", Namespace: metav1.NamespaceDefault}}
	fnOther := fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fo", Namespace: "myns"}}
	ts := newShapeTS(t, []fv1.Function{fnDefault, fnOther}, nil)
	_, internal, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	assert.True(t, muxMatches(internal, http.MethodPost, "/fission-function/fd"), "default ns must fold")
	assert.True(t, muxMatches(internal, http.MethodPost, "/fission-function/fd/sub"), "prefix subtree must route")
	assert.False(t, muxMatches(internal, http.MethodPost, "/fission-function/default/fd"),
		"the unfolded default-ns form is never registered")
	assert.True(t, muxMatches(internal, http.MethodPost, "/fission-function/myns/fo"))
	assert.True(t, muxMatches(internal, http.MethodPost, "/fission-function/myns/fo/sub"))
	assert.False(t, muxMatches(internal, http.MethodPost, "/fission-function/myns/foX"),
		"internal prefix must not glob past the path-segment boundary")
}

// TestRouteShapeGorillaTemplates pins gorilla path-template support
// ({var}, {var:regex}) through the shared registration helpers: the route
// table treats the template as an opaque shape string and gorilla compiles
// it at registration, so patterned RelativeURLs must keep matching exactly
// as they always have (real-world example: /bank/{html:[a-zA-Z0-9\.\/]+}).
func TestRouteShapeGorillaTemplates(t *testing.T) {
	ts := newShapeTS(t, []fv1.Function{shapeFn("fn")}, []fv1.HTTPTrigger{
		shapeTrigger("regex", func(tr *fv1.HTTPTrigger) {
			tr.Spec.RelativeURL = `/bank/{html:[a-zA-Z0-9\.\/]+}`
		}),
		shapeTrigger("var", func(tr *fv1.HTTPTrigger) {
			tr.Spec.RelativeURL = "/accounts/{id}"
		}),
	})
	public, _, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	assert.True(t, muxMatches(public, http.MethodGet, "/bank/index.html"), "regex template must match a file path")
	assert.True(t, muxMatches(public, http.MethodGet, "/bank/css/style.css"), "the regex includes / so it spans segments")
	assert.False(t, muxMatches(public, http.MethodGet, "/bank/oops!"), "characters outside the regex class must not match")
	assert.False(t, muxMatches(public, http.MethodPost, "/bank/index.html"), "methods still gate template routes")

	assert.True(t, muxMatches(public, http.MethodGet, "/accounts/123"), "plain {var} must match one segment")
	assert.False(t, muxMatches(public, http.MethodGet, "/accounts/123/txns"), "plain {var} must not span segments")
}
