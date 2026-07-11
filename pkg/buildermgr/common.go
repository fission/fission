// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dchest/uniuri"
	"github.com/go-logr/logr"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/builder"
	builderClient "github.com/fission/fission/pkg/builder/client"
	"github.com/fission/fission/pkg/conditions"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/fetcher"
	fetcherClient "github.com/fission/fission/pkg/fetcher/client"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	storagesvcClient "github.com/fission/fission/pkg/storagesvc/client"
	"github.com/fission/fission/pkg/svcinfo"
	"github.com/fission/fission/pkg/utils"
)

// builderSigningNamespace decides whether buildermgr signs a builder pod's
// fetcher/builder sidecar calls with that namespace's per-namespace derived key.
// Version-aware, the sibling of the executor's poolmgr.fetcherSigningNamespace:
// only a builder pod stamped with the namespace key-scheme annotation (created by
// createBuilderDeployment while per-namespace keys were in use for its namespace)
// holds the per-namespace keys and verifies with them, so we sign with the builder
// namespace key. Pre-upgrade pods carrying no annotation, and every pod under
// static tenancy, verify with the master-derived key and stay master-signed.
func builderSigningNamespace(pod *apiv1.Pod, builderNs string) (string, bool) {
	if pod != nil && utils.PerNamespaceKeysEnabled() && fv1.HasNamespaceKeyScheme(pod.Annotations) {
		return builderNs, true
	}
	return "", false
}

// buildPackage helps to build source package into deployment package.
// Following is the steps buildPackage function takes to complete the whole process.
// 1. Send fetch request to fetcher to fetch source package.
// 2. Send build request to builder to start a build.
// 3. Send upload request to fetcher to upload deployment package.
// 4. Return upload response and build logs.
// *. Return build logs and error if any one of steps above failed.
// signNamespace selects version-aware signing for the builder pod's sidecars:
// non-empty means sign with that namespace's per-namespace keys (the pod holds
// only those), empty means the master-derived keys. The package reconciler
// computes it from the ready builder pod's key-scheme annotation
// (builderSigningNamespace).
func buildPackage(ctx context.Context, logger logr.Logger, fissionClient versioned.Interface, envBuilderNamespace string,
	signNamespace string, storageSvcUrl string, registryCfg *packageRegistryConfig, pkg *fv1.Package) (uploadResp *fetcher.ArchiveUploadResponse, buildLogs string, err error) {

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
	fetcherURL := fmt.Sprintf("http://%s:%d", svcName, svcinfo.PortFetcher)
	builderURL := fmt.Sprintf("http://%s:%d", svcName, svcinfo.PortBuilder)
	var fetcherC fetcherClient.ClientInterface
	var builderC builderClient.ClientInterface
	if signNamespace != "" {
		fetcherC = fetcherClient.MakeClientNS(logger, fetcherURL, masterSecret, signNamespace)
		builderC = builderClient.MakeClientNS(logger, builderURL, masterSecret, signNamespace)
	} else {
		fetcherC = fetcherClient.MakeClient(logger, fetcherURL, masterSecret)
		builderC = builderClient.MakeClient(logger, builderURL, masterSecret)
	}

	defer func() {
		logger.Info("cleaning src pkg from builder storage", "source_package", srcPkgFilename)
		if errC := cleanPackage(ctx, builderC, srcPkgFilename); errC != nil {
			if ferror.IsNotFound(errC) {
				// Defensive: today's builder Clean never 404s (os.RemoveAll
				// is idempotent), but a future handler that does shouldn't
				// log "already gone" as an error.
				return
			}
			logger.Error(errC, "error cleaning src pkg from builder storage",
				"source_package", srcPkgFilename,
				"package", pkg.Name, "package_namespace", pkg.Namespace)
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
		// RFC-0012 producer: publish the deployment directory as an OCI
		// image when a registry is configured (nil keeps today's tarball).
		OCIPush: registryCfg.ociPushSpecFor(pkg, env),
	}

	logger.Info("started uploading deployment package", "deployment_package", buildResp.ArtifactFilename)
	// ask fetcher to upload the deployment package
	uploadResp, err = fetcherC.Upload(ctx, uploadReq)
	if err != nil {
		e := fmt.Sprintf("Error uploading deployment package: %v", err)
		buildResp.BuildLogs += fmt.Sprintf("%v\n", e)
		return nil, buildResp.BuildLogs, ferror.MakeError(http.StatusInternalServerError, e)
	}
	if uploadResp.OCI != nil && registryCfg != nil && registryCfg.pullSecret != "" {
		// The read credential is stamped control-plane-side: the fetcher
		// only knows the push secret (least privilege, deliberately
		// distinct).
		uploadResp.OCI.ImagePullSecrets = []apiv1.LocalObjectReference{{Name: registryCfg.pullSecret}}
	}
	switch {
	case uploadResp.OCI != nil:
		recordOCIPublish(ctx, "published")
	case uploadResp.OCIPushError != "":
		// The control-plane signal for a degraded producer: the per-package
		// condition and builder-pod logs alone would leave a fleet-wide
		// registry outage invisible on dashboards.
		logger.Error(errors.New(uploadResp.OCIPushError), "OCI publish degraded to the storage tarball",
			"package", pkg.Name, "namespace", pkg.Namespace)
		recordOCIPublish(ctx, "degraded")
		buildResp.BuildLogs += fmt.Sprintf("OCI publish failed (fell back to the storage tarball): %v\n", uploadResp.OCIPushError)
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

	packages := fissionClient.CoreV1().Packages(pkg.Namespace)
	name := pkg.Name

	// A successful build also writes the deployment archive onto the spec. With
	// the /status subresource enabled, the apiserver ignores status on a
	// main-resource Update, so persist the spec first (it may bump
	// metadata.generation) and build the status afterwards, so the conditions'
	// ObservedGeneration is stamped from the post-update generation. Both writes
	// re-get on conflict, since other actors (a concurrent reconcile, a CLI
	// rebuild) may update the package between our read and write.
	if uploadResp != nil {
		deployment := fv1.Archive{
			Type:     fv1.ArchiveTypeUrl,
			URL:      uploadResp.ArchiveDownloadUrl,
			Checksum: uploadResp.Checksum,
		}
		if uploadResp.OCI != nil {
			// RFC-0012 producer: the build was published as a digest-pinned
			// OCI image; the Package's deployment archive references it
			// instead of a storagesvc URL.
			deployment = fv1.Archive{
				Type: fv1.ArchiveTypeOCI,
				OCI:  uploadResp.OCI,
			}
		}
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			fresh, gerr := packages.Get(ctx, name, metav1.GetOptions{})
			if gerr != nil {
				return gerr
			}
			fresh.Spec.Deployment = deployment
			var uerr error
			pkg, uerr = packages.Update(ctx, fresh, metav1.UpdateOptions{})
			return uerr
		}); err != nil {
			e := "error updating package spec"
			logger.Error(err, e)
			return nil, fmt.Errorf("%s: %w", e, err)
		}
	}

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh, gerr := packages.Get(ctx, name, metav1.GetOptions{})
		if gerr != nil {
			return gerr
		}
		// Preserve existing Conditions across the status replacement so
		// transitions aren't accidentally wiped when build outcome changes.
		existingConds := fresh.Status.Conditions
		fresh.Status = fv1.PackageStatus{
			BuildStatus:         status,
			BuildLog:            buildLogs,
			LastUpdateTimestamp: metav1.Time{Time: time.Now().UTC()},
			Conditions:          existingConds,
		}
		setPackageBuildCondition(&fresh.Status, status, buildLogs, fresh.Generation)
		setPackageOCIPublishCondition(&fresh.Status, uploadResp, fresh.Generation)
		var uerr error
		pkg, uerr = packages.UpdateStatus(ctx, fresh, metav1.UpdateOptions{})
		return uerr
	}); err != nil {
		e := "error updating package status"
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
		readyMessage = "unrecognised BuildStatus value: " + string(status)
	}
	// Both conditions get the short, fixed-per-status `readyMessage`
	// instead of the raw build log. The full builder output already
	// lives in Status.BuildLog (untruncated); duplicating a 32 KB blob
	// into Condition.Message would just inflate every Package payload
	// without giving users new information.
	conditions.Set(&s.Conditions, metav1.Condition{
		Type:               fv1.PackageConditionBuildSucceeded,
		Status:             buildStatus,
		ObservedGeneration: gen,
		Reason:             reason,
		Message:            readyMessage,
	})
	conditions.Set(&s.Conditions, metav1.Condition{
		Type:               fv1.PackageConditionReady,
		Status:             readyStatus,
		ObservedGeneration: gen,
		Reason:             reason,
		Message:            readyMessage,
	})
}

// setPackageOCIPublishCondition records the RFC-0012 producer outcome on the
// Package: published (True), degraded to the tarball fallback (False), or —
// when the build did not involve the producer — no change (a stale condition
// from an earlier producer build is cleared so it cannot misrepresent the
// current archive).
func setPackageOCIPublishCondition(s *fv1.PackageStatus, uploadResp *fetcher.ArchiveUploadResponse, gen int64) {
	if uploadResp == nil {
		return
	}
	switch {
	case uploadResp.OCI != nil:
		conditions.Set(&s.Conditions, metav1.Condition{
			Type: fv1.PackageConditionOCIPublished, Status: metav1.ConditionTrue,
			ObservedGeneration: gen, Reason: fv1.PackageReasonOCIPublished,
			Message: fmt.Sprintf("published as %s (%s)", uploadResp.OCI.Image, uploadResp.OCI.Digest),
		})
	case uploadResp.OCIPushError != "":
		conditions.Set(&s.Conditions, metav1.Condition{
			Type: fv1.PackageConditionOCIPublished, Status: metav1.ConditionFalse,
			ObservedGeneration: gen, Reason: fv1.PackageReasonOCIPublishDegraded,
			Message: conditions.TruncateMessage("push failed; the build fell back to the storage tarball: " + uploadResp.OCIPushError),
		})
	default:
		// Tarball build without producer involvement: drop any stale
		// publish condition from a previous registry-enabled build.
		conditions.Delete(&s.Conditions, fv1.PackageConditionOCIPublished)
	}
}
