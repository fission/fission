// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package harness owns the cluster-side lifecycle of a benchmark run. Env is the
// shared per-run context (clients, capturer, router targets); Scope is the
// per-scenario resource lifecycle with its own idempotent cleanup. Resources are
// provisioned through the version-stable typed clientset so the same binary can
// drive HEAD or a released control plane.
package harness

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/test/benchmark/pkg/cluster"
	"github.com/fission/fission/test/benchmark/pkg/loadgen"
)

// Config parameterises a benchmark Env.
type Config struct {
	Kubeconfig        string // empty -> KUBECONFIG/in-cluster
	FissionNamespace  string // control-plane namespace (default "fission")
	WorkNamespace     string // namespace for benchmark CRs (default "default")
	RouterURL         string // public listener base, e.g. http://127.0.0.1:8888
	RouterInternalURL string // internal listener base, e.g. http://127.0.0.1:8889
	PrometheusURL     string // optional
	PprofTargets      map[string]string
	ArtifactDir       string // optional; pprof/range-query dumps land here
	RunID             string // unique per run; embedded in resource names
}

// Env is the shared per-run context handed to every scenario.
type Env struct {
	Clients     *cluster.Clients
	Capturer    *cluster.Capturer
	Images      Images
	Namespace   string // work namespace
	ArtifactDir string // optional output dir for captures
	RunID       string

	fissionNamespace  string
	routerURL         string
	routerInternalURL string
	signWrap          func(http.RoundTripper) http.RoundTripper
}

// New builds an Env: clients, internal-auth secret resolution, and the capturer.
func New(ctx context.Context, cfg Config) (*Env, error) {
	if cfg.FissionNamespace == "" {
		cfg.FissionNamespace = "fission"
	}
	if cfg.WorkNamespace == "" {
		cfg.WorkNamespace = "default"
	}
	clients, err := cluster.Connect(cfg.Kubeconfig)
	if err != nil {
		return nil, err
	}
	// The internal-auth secret is only needed for the optional signed
	// internal-listener path (InternalTarget). Resolution failures must not
	// abort a run that benchmarks the public router, so degrade to unsigned.
	master, err := cluster.InternalAuthSecret(ctx, clients.Kube, cfg.FissionNamespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: internal auth secret unavailable, internal-listener signing disabled: %v\n", err)
		master = nil
	}
	return &Env{
		Clients:           clients,
		Capturer:          &cluster.Capturer{PrometheusURL: cfg.PrometheusURL, PprofTargets: cfg.PprofTargets},
		Images:            LoadImages(),
		Namespace:         cfg.WorkNamespace,
		ArtifactDir:       cfg.ArtifactDir,
		RunID:             cfg.RunID,
		fissionNamespace:  cfg.FissionNamespace,
		routerURL:         strings.TrimRight(cfg.RouterURL, "/"),
		routerInternalURL: strings.TrimRight(cfg.RouterInternalURL, "/"),
		signWrap:          cluster.SigningTransportWrapper(master),
	}, nil
}

// FissionNamespace returns the control-plane namespace, for PromQL that targets
// the Fission components themselves (e.g. client-side apiserver call counters).
func (e *Env) FissionNamespace() string { return e.fissionNamespace }

// NewScope returns a per-scenario resource scope; label disambiguates resource
// names and cleanup logs across scenarios sharing the Env.
func (e *Env) NewScope(label string) *Scope {
	return &Scope{env: e, label: label}
}

// PublicTarget builds a loadgen HTTP target against the public router for a
// registered HTTPTrigger relative URL — the real user data path (no signing).
func (e *Env) PublicTarget(relativeURL string, concurrency int, keepAlive bool) *loadgen.HTTPTarget {
	return e.PublicTargetFull(relativeURL, http.MethodGet, nil, concurrency, keepAlive)
}

// PublicTargetFull is PublicTarget with an explicit method and request body
// (used by the payload-size sweep).
func (e *Env) PublicTargetFull(relativeURL, method string, body []byte, concurrency int, keepAlive bool) *loadgen.HTTPTarget {
	return loadgen.NewHTTPTarget(loadgen.HTTPTargetConfig{
		URL:         e.routerURL + relativeURL,
		Method:      method,
		Body:        body,
		Concurrency: concurrency,
		KeepAlive:   keepAlive,
		Timeout:     60 * time.Second,
	})
}

// InternalTarget builds a loadgen HTTP target against the router internal
// listener for a function, signing requests when an auth secret is present.
func (e *Env) InternalTarget(fnName string, concurrency int, keepAlive bool) *loadgen.HTTPTarget {
	return loadgen.NewHTTPTarget(loadgen.HTTPTargetConfig{
		URL:           e.routerInternalURL + utils.UrlForFunction(fnName, e.Namespace),
		Concurrency:   concurrency,
		KeepAlive:     keepAlive,
		Timeout:       60 * time.Second,
		WrapTransport: e.signWrap,
	})
}

// InternalAPITarget builds a loadgen target for a router internal-listener API
// path with a query string (e.g. the RFC-0027 topic admin surface), signed
// like InternalTarget.
func (e *Env) InternalAPITarget(pathAndQuery, method string, body []byte, headers http.Header, concurrency int) *loadgen.HTTPTarget {
	return loadgen.NewHTTPTarget(loadgen.HTTPTargetConfig{
		URL:           e.routerInternalURL + pathAndQuery,
		Method:        method,
		Body:          body,
		Headers:       headers,
		Concurrency:   concurrency,
		KeepAlive:     true,
		Timeout:       60 * time.Second,
		WrapTransport: e.signWrap,
	})
}

// RouterURL returns the public router base URL.
func (e *Env) RouterURL() string { return e.routerURL }
