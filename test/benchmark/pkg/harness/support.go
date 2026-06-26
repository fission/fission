// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// Defer registers an arbitrary cleanup with the scope, run in reverse order by
// Cleanup. Scenarios that create raw Kubernetes objects (e.g. synthetic
// Services/EndpointSlices) use this to schedule their deletion.
func (s *Scope) Defer(name string, fn func(context.Context) error) {
	s.addCleanup(name, fn)
}

// CreateSourcePackage creates a Package with a source archive for the builder to
// compile. The status subresource is left unset on Create; buildermgr derives
// "pending" from the non-empty Source and runs the build.
func (s *Scope) CreateSourcePackage(ctx context.Context, name, envName string, srcZip []byte, buildCmd string) error {
	ns := s.env.Namespace
	pkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: fv1.PackageSpec{
			Environment:  fv1.EnvironmentReference{Namespace: ns, Name: envName},
			Source:       fv1.Archive{Type: fv1.ArchiveTypeLiteral, Literal: srcZip},
			BuildCommand: buildCmd,
		},
		// A buildable package starts pending; set it on the submitted object so
		// older validating webhooks (<= v1.24) that reject an empty BuildStatus
		// accept it (the status subresource is stripped on Create regardless).
		Status: fv1.PackageStatus{BuildStatus: fv1.BuildStatusPending},
	}
	if _, err := s.env.Clients.Fission.CoreV1().Packages(ns).Create(ctx, pkg, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create source package %q: %w", name, err)
	}
	s.addCleanup("package "+name, func(c context.Context) error {
		return ignoreNotFound(s.env.Clients.Fission.CoreV1().Packages(ns).Delete(c, name, metav1.DeleteOptions{}))
	})
	return nil
}

// WaitForPackageBuild polls a package until it reaches a terminal build status,
// returning the elapsed time on success or an error (including a fast-fail on
// BuildStatusFailed with the build log).
func (e *Env) WaitForPackageBuild(ctx context.Context, name string, timeout time.Duration) (time.Duration, error) {
	start := time.Now()
	err := Poll(ctx, timeout, 2*time.Second, func(ctx context.Context) (bool, error) {
		pkg, err := e.Clients.Fission.CoreV1().Packages(e.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		switch pkg.Status.BuildStatus {
		case fv1.BuildStatusSucceeded, fv1.BuildStatusNone:
			return true, nil
		case fv1.BuildStatusFailed:
			return false, fmt.Errorf("package %q build failed: %s", name, pkg.Status.BuildLog)
		default:
			return false, nil
		}
	})
	return time.Since(start), err
}

// CountReadyFunctionPods returns the number of Ready pods for a function, across
// all namespaces (newdeploy/poolmgr-specialized pods carry the functionName
// label). Used by autoscaling scenarios to observe replica growth.
func (e *Env) CountReadyFunctionPods(ctx context.Context, fnName string) (int, error) {
	selector := labels.SelectorFromSet(labels.Set{fv1.FUNCTION_NAME: fnName}).String()
	pods, err := e.Clients.Kube.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return 0, err
	}
	return countReady(pods.Items), nil
}
