/*
Copyright 2017 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package buildermgr

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dchest/uniuri"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/builder"
	builderClient "github.com/fission/fission/pkg/builder/client"
	"github.com/fission/fission/pkg/conditions"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/fetcher"
	fetcherClient "github.com/fission/fission/pkg/fetcher/client"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	storagesvcClient "github.com/fission/fission/pkg/storagesvc/client"
)

// buildPackage helps to build source package into deployment package.
// Following is the steps buildPackage function takes to complete the whole process.
// 1. Send fetch request to fetcher to fetch source package.
// 2. Send build request to builder to start a build.
// 3. Send upload request to fetcher to upload deployment package.
// 4. Return upload response and build logs.
// *. Return build logs and error if any one of steps above failed.
func buildPackage(ctx context.Context, logger logr.Logger, fissionClient versioned.Interface, envBuilderNamespace string,
	storageSvcUrl string, pkg *fv1.Package) (uploadResp *fetcher.ArchiveUploadResponse, buildLogs string, err error) {

	// Defence in depth against cross-namespace Environment references; the
	// admission webhook is the user-visible reject, this guard catches
	// objects that bypassed it (GHSA-vjhc-cf4p-72q4).
	if pkg.Spec.Environment.Namespace != "" && pkg.Spec.Environment.Namespace != pkg.Namespace {
		e := fmt.Sprintf("cross-namespace environment reference is not allowed: pkg.namespace=%s env.namespace=%s",
			pkg.Namespace, pkg.Spec.Environment.Namespace)
		return nil, e, ferror.MakeError(ferror.ErrorInvalidArgument, e)
	}

	env, err := fissionClient.CoreV1().Environments(pkg.Spec.Environment.Namespace).Get(ctx, pkg.Spec.Environment.Name, metav1.GetOptions{})
	if err != nil {
		e := "error getting environment CRD info"
		logger.Error(err, e)
		e = fmt.Sprintf("%s: %v", e, err)
		return nil, e, ferror.MakeError(http.StatusInternalServerError, e)
	}

	svcName := fmt.Sprintf("%s-%s.%s", env.Name, env.ResourceVersion, envBuilderNamespace)
	srcPkgFilename := fmt.Sprintf("%s-%s", pkg.Name, strings.ToLower(uniuri.NewLen(6)))
	// HMACSecretFromEnv returns nil when internalAuth is disabled; the
	// fetcher / builder clients pass-through unsigned in that case,
	// which matches the corresponding verifier's empty-secret short-
	// circuit on the server side.
	masterSecret := storagesvcClient.HMACSecretFromEnv()
	fetcherC := fetcherClient.MakeClient(logger, fmt.Sprintf("http://%s:8000", svcName), masterSecret)
	builderC := builderClient.MakeClient(logger, fmt.Sprintf("http://%s:8001", svcName), masterSecret)

	defer func() {
		logger.Info("cleaning src pkg from builder storage", "source_package", srcPkgFilename)
		errC := cleanPackage(ctx, builderC, srcPkgFilename)
		if errC != nil {
			m := "error cleaning src pkg from builder storage"
			logger.Error(errC, m)
		}
	}()

	fetchReq := &fetcher.FunctionFetchRequest{
		FetchType:   fv1.FETCH_SOURCE,
		Package:     pkg.ObjectMeta,
		Filename:    srcPkgFilename,
		KeepArchive: false,
	}

	// send fetch request to fetcher
	err = fetcherC.Fetch(ctx, fetchReq)
	if err != nil {
		e := "error fetching source package"
		logger.Error(err, e)
		e = fmt.Sprintf("%s: %v", e, err)
		return nil, e, ferror.MakeError(http.StatusInternalServerError, e)
	}

	buildCmd := pkg.Spec.BuildCommand
	if len(buildCmd) == 0 {
		buildCmd = env.Spec.Builder.Command
	}

	pkgBuildReq := &builder.PackageBuildRequest{
		SrcPkgFilename: srcPkgFilename,
		BuildCommand:   buildCmd,
	}

	logger.Info("started building with source package", "source_package", srcPkgFilename)
	// send build request to builder
	buildResp, err := builderC.Build(ctx, pkgBuildReq)
	if err != nil {
		e := fmt.Sprintf("Error building deployment package: %v", err)
		var buildLogs string
		if buildResp != nil {
			buildLogs = buildResp.BuildLogs
		}
		buildLogs += fmt.Sprintf("%v\n", e)
		return nil, buildLogs, ferror.MakeError(http.StatusInternalServerError, e)
	}

	logger.Info("build succeed", "source_package", srcPkgFilename, "deployment_package", buildResp.ArtifactFilename)

	archivePackage := !env.Spec.KeepArchive

	uploadReq := &fetcher.ArchiveUploadRequest{
		Filename:       buildResp.ArtifactFilename,
		StorageSvcUrl:  storageSvcUrl,
		ArchivePackage: archivePackage,
	}

	logger.Info("started uploading deployment package", "deployment_package", buildResp.ArtifactFilename)
	// ask fetcher to upload the deployment package
	uploadResp, err = fetcherC.Upload(ctx, uploadReq)
	if err != nil {
		e := fmt.Sprintf("Error uploading deployment package: %v", err)
		buildResp.BuildLogs += fmt.Sprintf("%v\n", e)
		return nil, buildResp.BuildLogs, ferror.MakeError(http.StatusInternalServerError, e)
	}

	return uploadResp, buildResp.BuildLogs, nil
}

func cleanPackage(ctx context.Context, builderClient builderClient.ClientInterface, srcPkgFileName string) error {
	err := builderClient.Clean(ctx, srcPkgFileName)
	if err != nil {
		return err
	}

	return nil
}

func updatePackage(ctx context.Context, logger logr.Logger, fissionClient versioned.Interface,
	pkg *fv1.Package, status fv1.BuildStatus, buildLogs string,
	uploadResp *fetcher.ArchiveUploadResponse) (*fv1.Package, error) {

	// Preserve existing Conditions across the status replacement so
	// transitions aren't accidentally wiped when build outcome changes.
	existingConds := pkg.Status.Conditions
	pkg.Status = fv1.PackageStatus{
		BuildStatus:         status,
		BuildLog:            buildLogs,
		LastUpdateTimestamp: metav1.Time{Time: time.Now().UTC()},
		Conditions:          existingConds,
	}
	setPackageBuildCondition(&pkg.Status, status, buildLogs, pkg.Generation)

	if uploadResp != nil {
		pkg.Spec.Deployment = fv1.Archive{
			Type:     fv1.ArchiveTypeUrl,
			URL:      uploadResp.ArchiveDownloadUrl,
			Checksum: uploadResp.Checksum,
		}
	}

	// update package spec
	pkg, err := fissionClient.CoreV1().Packages(pkg.ObjectMeta.Namespace).Update(ctx, pkg, metav1.UpdateOptions{})
	if err != nil {
		e := "error updating package"
		logger.Error(err, e)
		return nil, fmt.Errorf("%s: %w", e, err)
	}

	// return resource version for function to update function package ref
	return pkg, nil
}

// markFunctionsForPackage writes PackageReady + Ready conditions on every
// Function in fns that references pkg. Used by the buildermgr to propagate
// build outcome onto the dependent Functions' status subresources so users
// can `kubectl wait --for=condition=Ready function/<name>`. Best-effort —
// individual function writes log at V(1) on failure and keep going.
//
// Fast-path: each function's in-memory Conditions are checked first; we
// only Get + UpdateStatus when the new state would actually transition
// (matching the user's "key transitions only" expectation).
func markFunctionsForPackage(ctx context.Context, logger logr.Logger, fissionClient versioned.Interface, fns []fv1.Function, pkg *fv1.Package, succeeded bool) {
	condStatus := metav1.ConditionFalse
	reason, message := fv1.FunctionReasonPackageFailed, "package build failed; see Package.Status.BuildLog"
	if succeeded {
		condStatus = metav1.ConditionTrue
		reason, message = fv1.FunctionReasonPackageReady, "package built and ready to deploy"
	}
	for i := range fns {
		fn := &fns[i]
		if fn.Spec.Package.PackageRef.Name != pkg.Name || fn.Spec.Package.PackageRef.Namespace != pkg.Namespace {
			continue
		}
		wantPkg := metav1.Condition{
			Type: fv1.FunctionConditionPackageReady, Status: condStatus,
			ObservedGeneration: fn.Generation, Reason: reason, Message: message,
		}
		wantReady := metav1.Condition{
			Type: fv1.FunctionConditionReady, Status: condStatus,
			ObservedGeneration: fn.Generation, Reason: reason, Message: message,
		}
		if conditions.IsAt(fn.Status.Conditions, wantPkg) && conditions.IsAt(fn.Status.Conditions, wantReady) {
			continue
		}
		cur, err := fissionClient.CoreV1().Functions(fn.Namespace).Get(ctx, fn.Name, metav1.GetOptions{})
		if err != nil {
			logger.V(1).Info("function status: get failed", "name", fn.Name, "namespace", fn.Namespace, "error", err)
			continue
		}
		wantPkg.ObservedGeneration = cur.Generation
		wantReady.ObservedGeneration = cur.Generation
		if conditions.IsAt(cur.Status.Conditions, wantPkg) && conditions.IsAt(cur.Status.Conditions, wantReady) {
			continue
		}
		conditions.Set(&cur.Status.Conditions, wantPkg)
		conditions.Set(&cur.Status.Conditions, wantReady)
		if _, err := fissionClient.CoreV1().Functions(fn.Namespace).UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err != nil {
			logger.V(1).Info("function status: update failed", "name", fn.Name, "namespace", fn.Namespace, "error", err)
		}
	}
}

// conditionMessageMaxLen is the upper bound on metav1.Condition.Message
// enforced by the apiserver via the generated CRD schema (maxLength=32768
// — see crds/v1/fission.io_packages.yaml). Any longer message would cause
// the entire UpdateStatus to be rejected. Leave a small headroom for any
// future copy/edit churn.
const conditionMessageMaxLen = 32 * 1024

// truncateForCondition trims s to conditionMessageMaxLen, appending an
// elision marker when truncation occurred so the consumer knows to fetch
// the full text elsewhere (e.g., Package.Status.BuildLog still has the
// untruncated build output).
func truncateForCondition(s string) string {
	if len(s) <= conditionMessageMaxLen {
		return s
	}
	const ellipsis = "... [truncated; see full text on parent resource]"
	keep := conditionMessageMaxLen - len(ellipsis)
	if keep < 0 {
		keep = 0
	}
	return s[:keep] + ellipsis
}

// setPackageBuildCondition mirrors the legacy BuildStatus enum onto the new
// PackageBuildSucceeded and Ready conditions so `kubectl wait
// --for=condition=Ready package/<name>` works alongside the existing
// BuildStatus string. The mapping follows the same terminal-state semantics
// the buildermgr already uses for BuildStatus.
func setPackageBuildCondition(s *fv1.PackageStatus, status fv1.BuildStatus, buildLogs string, gen int64) {
	var (
		buildStatus, readyStatus metav1.ConditionStatus
		reason, readyMessage     string
	)
	switch status {
	case fv1.BuildStatusSucceeded:
		buildStatus, readyStatus = metav1.ConditionTrue, metav1.ConditionTrue
		reason = fv1.PackageReasonBuildSucceeded
		readyMessage = "package built and ready to deploy"
	case fv1.BuildStatusFailed:
		buildStatus, readyStatus = metav1.ConditionFalse, metav1.ConditionFalse
		reason = fv1.PackageReasonBuildFailed
		readyMessage = "package build failed; see Package.Status.BuildLog"
	case fv1.BuildStatusNone:
		// Deploy-only packages have nothing to build. They're ready by
		// virtue of the deployment archive being supplied.
		buildStatus, readyStatus = metav1.ConditionTrue, metav1.ConditionTrue
		reason = fv1.PackageReasonNoBuildRequired
		readyMessage = "package has a pre-built deployment archive"
	case fv1.BuildStatusPending:
		buildStatus, readyStatus = metav1.ConditionUnknown, metav1.ConditionFalse
		reason = fv1.PackageReasonBuildPending
		readyMessage = "package build pending"
	case fv1.BuildStatusRunning:
		buildStatus, readyStatus = metav1.ConditionUnknown, metav1.ConditionFalse
		reason = fv1.PackageReasonBuildRunning
		readyMessage = "package build in progress"
	default:
		buildStatus, readyStatus = metav1.ConditionUnknown, metav1.ConditionUnknown
		reason = fv1.PackageReasonUnknown
	}
	conditions.Set(&s.Conditions, metav1.Condition{
		Type:               fv1.PackageConditionBuildSucceeded,
		Status:             buildStatus,
		ObservedGeneration: gen,
		Reason:             reason,
		// Truncate so this UpdateStatus isn't rejected by the apiserver
		// for exceeding the standard Condition.message 32KB cap. The full
		// build output remains in Status.BuildLog.
		Message: truncateForCondition(buildLogs),
	})
	conditions.Set(&s.Conditions, metav1.Condition{
		Type:               fv1.PackageConditionReady,
		Status:             readyStatus,
		ObservedGeneration: gen,
		Reason:             reason,
		Message:            readyMessage,
	})
}
