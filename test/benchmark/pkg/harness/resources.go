// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"context"
	"fmt"
	"net/http"
	"os"

	apiv1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// EnvOptions describes a benchmark environment.
type EnvOptions struct {
	Name     string
	Image    string // runtime image (required)
	Builder  string // optional builder image
	Version  int    // env contract version; default 2
	Poolsize int    // poolmgr warm pool; default 3

	MinCPU, MaxCPU       int // millicores; 0 omits
	MinMemory, MaxMemory int // MiB; 0 omits
	GracePeriod          int64
}

// CreateEnv creates an Environment and registers its cleanup.
func (s *Scope) CreateEnv(ctx context.Context, o EnvOptions) error {
	ns := s.env.Namespace
	if o.Version == 0 {
		o.Version = 2
	}
	if o.Poolsize == 0 {
		o.Poolsize = 3
	}
	env := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: o.Name, Namespace: ns},
		Spec: fv1.EnvironmentSpec{
			Version:                o.Version,
			Runtime:                fv1.Runtime{Image: o.Image},
			Poolsize:               o.Poolsize,
			Resources:              resourceRequirements(o.MinCPU, o.MaxCPU, o.MinMemory, o.MaxMemory),
			TerminationGracePeriod: o.GracePeriod,
		},
	}
	if o.Builder != "" {
		env.Spec.Builder = fv1.Builder{Image: o.Builder}
	}
	if _, err := s.env.Clients.Fission.CoreV1().Environments(ns).Create(ctx, env, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create environment %q: %w", o.Name, err)
	}
	s.addCleanup("env "+o.Name, func(c context.Context) error {
		return ignoreNotFound(s.env.Clients.Fission.CoreV1().Environments(ns).Delete(c, o.Name, metav1.DeleteOptions{}))
	})
	return nil
}

// FunctionOptions describes a code (literal-package) function.
type FunctionOptions struct {
	Name         string
	Env          string
	Code         []byte // literal deployment archive (raw, < 256KiB)
	Entrypoint   string // FunctionName in the package ref (e.g. "main")
	ExecutorType fv1.ExecutorType

	MinScale, MaxScale   int // newdeploy/container only
	TargetCPUPercent     int // newdeploy HPA target; 0 omits
	Concurrency          int // default 500
	RequestsPerPod       int // default 1
	FunctionTimeout      int // seconds; default 60
	MinCPU, MaxCPU       int
	MinMemory, MaxMemory int
}

// CreateCodeFunction creates a literal Package and a Function referencing it,
// registering both cleanups (function first so it is deleted before its package).
func (s *Scope) CreateCodeFunction(ctx context.Context, o FunctionOptions) error {
	ns := s.env.Namespace
	if o.ExecutorType == "" {
		o.ExecutorType = fv1.ExecutorTypePoolmgr
	}
	if o.MaxScale == 0 {
		o.MaxScale = 1
	}
	if o.Concurrency == 0 {
		o.Concurrency = 500
	}
	if o.RequestsPerPod == 0 {
		o.RequestsPerPod = 1
	}
	if o.FunctionTimeout == 0 {
		o.FunctionTimeout = 60
	}

	pkgName := o.Name + "-pkg"
	pkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: pkgName, Namespace: ns},
		Spec: fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{Namespace: ns, Name: o.Env},
			Deployment:  fv1.Archive{Type: fv1.ArchiveTypeLiteral, Literal: o.Code},
		},
		// Set a valid BuildStatus on the submitted object: the API server strips
		// the status subresource on Create (so we still UpdateStatus below), but
		// older validating webhooks (<= v1.24) reject an empty BuildStatus on the
		// submitted Package.
		Status: fv1.PackageStatus{BuildStatus: fv1.BuildStatusNone},
	}
	created, err := s.env.Clients.Fission.CoreV1().Packages(ns).Create(ctx, pkg, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create package %q: %w", pkgName, err)
	}
	s.addCleanup("package "+pkgName, func(c context.Context) error {
		return ignoreNotFound(s.env.Clients.Fission.CoreV1().Packages(ns).Delete(c, pkgName, metav1.DeleteOptions{}))
	})
	// Close the fetcher's build-status gate. On CRDs with a status subresource
	// Create stripped the status set above, so set it here; on CRDs without one
	// (older releases) Create already persisted it and this 404s — best-effort,
	// the status is already none.
	created.Status.BuildStatus = fv1.BuildStatusNone
	if _, err := s.env.Clients.Fission.CoreV1().Packages(ns).UpdateStatus(ctx, created, metav1.UpdateOptions{}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: set package %q build status (continuing): %v\n", pkgName, err)
	}

	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: o.Name, Namespace: ns},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{Namespace: ns, Name: o.Env},
			Package: fv1.FunctionPackageRef{
				PackageRef:   fv1.PackageRef{Namespace: ns, Name: pkgName},
				FunctionName: o.Entrypoint,
			},
			InvokeStrategy: fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:     o.ExecutorType,
					MinScale:         o.MinScale,
					MaxScale:         o.MaxScale,
					TargetCPUPercent: o.TargetCPUPercent,
				},
			},
			Resources:       resourceRequirements(o.MinCPU, o.MaxCPU, o.MinMemory, o.MaxMemory),
			FunctionTimeout: o.FunctionTimeout,
			Concurrency:     o.Concurrency,
			RequestsPerPod:  o.RequestsPerPod,
		},
	}
	if _, err := s.env.Clients.Fission.CoreV1().Functions(ns).Create(ctx, fn, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create function %q: %w", o.Name, err)
	}
	s.addCleanup("function "+o.Name, func(c context.Context) error {
		return ignoreNotFound(s.env.Clients.Fission.CoreV1().Functions(ns).Delete(c, o.Name, metav1.DeleteOptions{}))
	})
	return nil
}

// RouteOptions describes an HTTPTrigger.
type RouteOptions struct {
	Name            string
	Function        string         // single-function route
	FunctionWeights map[string]int // canary route (overrides Function)
	URL             string
	Methods         []string // allowed HTTP methods; defaults to [GET]
}

// CreateRoute creates an HTTPTrigger and registers its cleanup.
func (s *Scope) CreateRoute(ctx context.Context, o RouteOptions) error {
	ns := s.env.Namespace
	if len(o.Methods) == 0 {
		o.Methods = []string{http.MethodGet}
	}
	if o.Name == "" {
		o.Name = s.Name("route")
	}
	ref := fv1.FunctionReference{}
	if len(o.FunctionWeights) > 0 {
		ref.Type = fv1.FunctionReferenceTypeFunctionWeights
		ref.FunctionWeights = o.FunctionWeights
	} else {
		ref.Type = fv1.FunctionReferenceTypeFunctionName
		ref.Name = o.Function
	}
	trigger := &fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: o.Name, Namespace: ns},
		Spec: fv1.HTTPTriggerSpec{
			RelativeURL:       o.URL,
			Method:            o.Methods[0],
			Methods:           o.Methods,
			FunctionReference: ref,
		},
	}
	if _, err := s.env.Clients.Fission.CoreV1().HTTPTriggers(ns).Create(ctx, trigger, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create httptrigger %q: %w", o.Name, err)
	}
	name := o.Name
	s.addCleanup("httptrigger "+name, func(c context.Context) error {
		return ignoreNotFound(s.env.Clients.Fission.CoreV1().HTTPTriggers(ns).Delete(c, name, metav1.DeleteOptions{}))
	})
	return nil
}

func resourceRequirements(minCPU, maxCPU, minMem, maxMem int) apiv1.ResourceRequirements {
	rr := apiv1.ResourceRequirements{Requests: apiv1.ResourceList{}, Limits: apiv1.ResourceList{}}
	if minCPU > 0 {
		rr.Requests[apiv1.ResourceCPU] = *resource.NewMilliQuantity(int64(minCPU), resource.DecimalSI)
	}
	if maxCPU > 0 {
		rr.Limits[apiv1.ResourceCPU] = *resource.NewMilliQuantity(int64(maxCPU), resource.DecimalSI)
	}
	if minMem > 0 {
		rr.Requests[apiv1.ResourceMemory] = *resource.NewQuantity(int64(minMem)*1024*1024, resource.BinarySI)
	}
	if maxMem > 0 {
		rr.Limits[apiv1.ResourceMemory] = *resource.NewQuantity(int64(maxMem)*1024*1024, resource.BinarySI)
	}
	return rr
}

func ignoreNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
