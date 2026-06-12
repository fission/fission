// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestOCIPackageReconciles covers RFC-0001 Phase 1: an OCI package is a
// first-class Package CR that reconciles to BuildStatusNone (nothing to
// build) without touching any registry — the image reference deliberately
// does not exist because no data-path code runs for a package that no
// function invokes.
func TestOCIPackageReconciles(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "node-oci-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})

	pkgName := "oci-pkg-" + ns.ID
	const imageRef = "registry.invalid/example/hello-code:v1"
	ns.CreatePackage(t, ctx, framework.PackageOptions{
		Name: pkgName, Env: envName, OCI: imageRef,
	})

	pkg := ns.GetPackage(t, ctx, pkgName)
	assert.Equal(t, fv1.ArchiveTypeOCI, pkg.Spec.Deployment.Type)
	require.NotNil(t, pkg.Spec.Deployment.OCI)
	assert.Equal(t, imageRef, pkg.Spec.Deployment.OCI.Image)
	assert.Empty(t, pkg.Spec.Deployment.URL)
	assert.Empty(t, pkg.Spec.Deployment.Literal)
	assert.True(t, pkg.Spec.Source.IsEmpty(), "source archive must stay empty")

	// The buildermgr derives the initial status from the spec archives: an
	// OCI deployment archive means nothing to build.
	ns.WaitForPackageBuildStatus(t, ctx, pkgName, fv1.BuildStatusNone, 2*time.Minute)
	pkg = ns.GetPackage(t, ctx, pkgName)
	assert.Empty(t, pkg.Status.BuildLog, "no builder must have run for an OCI package")
}

// TestOCIPackageCELMutualExclusion proves the API server itself (CEL on the
// Archive schema) rejects a Package whose deployment archive sets both url
// and oci — defense in depth ahead of the webhook and CLI. CEL cannot cover
// combinations involving the byte-format literal field (see types.go); those
// are rejected by the webhook with the same message.
func TestOCIPackageCELMutualExclusion(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)

	pkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "oci-cel-" + ns.ID, Namespace: ns.Name},
		Spec: fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{Namespace: ns.Name, Name: "node"},
			Deployment: fv1.Archive{
				Type: fv1.ArchiveTypeOCI,
				URL:  "http://example.com/deploy.zip",
				OCI:  &fv1.OCIArchive{Image: "registry.invalid/example/hello-code:v1"},
			},
		},
	}
	_, err := f.FissionClient().CoreV1().Packages(ns.Name).Create(ctx, pkg, metav1.CreateOptions{})
	require.Error(t, err, "API server must reject url+oci on one archive")
	assert.Contains(t, err.Error(), "at most one of literal, url, or oci")
}

// pyHello returns a single-file python deployment whose main() returns body —
// the same layout buildHelloZip produces, so anything that serves via
// --deploy serves via OCI.
func pyHello(body string) map[string]string {
	return map[string]string{"hello.py": "def main():\n    return \"" + body + "\""}
}

// skipOnImageVolumeLeg gates the fetcher-pull (Path A) tests OFF the
// image-volume CI legs: there, eligible OCI functions switch to kubelet
// pulls, and these tests' image references (the registry's ClusterIP
// Service) only resolve via cluster DNS — which the kubelet doesn't use.
// Path A correctness is proven on the floor (1.32) leg instead.
func skipOnImageVolumeLeg(t *testing.T) {
	t.Helper()
	if os.Getenv("FISSION_TEST_IMAGE_VOLUME") != "" {
		t.Skip("image-volume leg: eligible OCI functions use kubelet pulls, which cannot resolve the in-cluster registry Service address")
	}
}

// TestOCIPackagePoolmgr covers RFC-0001 Path A end-to-end on the default
// poolmgr executor: the test pushes a code image to the in-cluster registry,
// creates an OCI package + function + route, and the fetcher pulls and
// extracts the image at specialization time.
func TestOCIPackagePoolmgr(t *testing.T) {
	t.Parallel()
	skipOnImageVolumeLeg(t)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	hostAddr, inclusterAddr := framework.RequireRegistry(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-oci-" + ns.ID
	pkgName := "oci-pm-pkg-" + ns.ID
	fnName := "fn-oci-pm-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime,
		MinCPU: 40, MaxCPU: 80, MinMemory: 64, MaxMemory: 128,
	})

	ref, _ := framework.PushCodeImage(t, hostAddr, inclusterAddr,
		"fission-test/hello-"+ns.ID, "v1", pyHello("Hello, OCI!"))

	ns.CreatePackage(t, ctx, framework.PackageOptions{Name: pkgName, Env: envName, OCI: ref})
	ns.WaitForPackageBuildStatus(t, ctx, pkgName, fv1.BuildStatusNone, time.Minute)

	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Pkg: pkgName, Entrypoint: "hello.main",
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("Hello, OCI!"))
	assert.Contains(t, body, "Hello, OCI!")
}

// TestOCIPackagePoolmgrDigestMismatch proves the digest pin is enforced on
// the pull path: a package pinning the wrong digest must never serve — the
// fetcher refuses the image and specialization fails.
func TestOCIPackagePoolmgrDigestMismatch(t *testing.T) {
	t.Parallel()
	skipOnImageVolumeLeg(t)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	hostAddr, inclusterAddr := framework.RequireRegistry(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-ocibad-" + ns.ID
	pkgName := "oci-bad-pkg-" + ns.ID
	fnName := "fn-oci-bad-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime,
		MinCPU: 40, MaxCPU: 80, MinMemory: 64, MaxMemory: 128,
	})

	ref, digest := framework.PushCodeImage(t, hostAddr, inclusterAddr,
		"fission-test/bad-digest-"+ns.ID, "v1", pyHello("never served"))
	wrong := "sha256:" + strings.Repeat("0", 64)
	require.NotEqual(t, digest, wrong)

	// The digest field has no CLI flag; create the Package CR directly.
	pkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: pkgName, Namespace: ns.Name},
		Spec: fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{Namespace: ns.Name, Name: envName},
			Deployment: fv1.Archive{
				Type: fv1.ArchiveTypeOCI,
				OCI:  &fv1.OCIArchive{Image: ref, Digest: wrong},
			},
		},
	}
	_, err := f.FissionClient().CoreV1().Packages(ns.Name).Create(ctx, pkg, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = f.FissionClient().CoreV1().Packages(ns.Name).Delete(context.Background(), pkgName, metav1.DeleteOptions{})
	})

	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Pkg: pkgName, Entrypoint: "hello.main",
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	// The invocation must never serve the function body. The router may
	// surface the specialization failure as a 5xx, or hold the request
	// while the executor retries until the client times out — both prove
	// non-service, so a transport error counts as failure-evidence too.
	sawFailure := false
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		status, body, err := f.Router(t).Get(ctx, "/"+fnName)
		if err != nil {
			sawFailure = true
			time.Sleep(time.Second)
			continue
		}
		require.NotContains(t, body, "never served", "wrong-digest image must not serve")
		if status >= 500 {
			sawFailure = true
			break
		}
		time.Sleep(time.Second)
	}
	require.True(t, sawFailure, "wrong-digest invocation must fail (5xx or hang), not succeed silently")
}

// TestOCIPackageNewdeploy covers RFC-0001 Path A on the newdeploy executor —
// which needs no Path A-specific executor code: newdeploy embeds the same
// shared specialize request, and the same in-pod fetcher pulls the image
// (the newdeploy OCI executor code is image-volume/Path B only). Warm
// (minscale 1) plus a cold-start (minscale 0) subtest.
func TestOCIPackageNewdeploy(t *testing.T) {
	t.Parallel()
	skipOnImageVolumeLeg(t)

	setupCtx, setupCancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer setupCancel()

	f := framework.Connect(t)
	hostAddr, inclusterAddr := framework.RequireRegistry(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-ocind-" + ns.ID
	ns.CreateEnv(t, setupCtx, framework.EnvOptions{
		Name: envName, Image: runtime,
		MinCPU: 40, MaxCPU: 80, MinMemory: 64, MaxMemory: 128,
	})

	ref, _ := framework.PushCodeImage(t, hostAddr, inclusterAddr,
		"fission-test/hello-nd-"+ns.ID, "v1", pyHello("Hello, newdeploy OCI!"))
	pkgName := "oci-nd-pkg-" + ns.ID
	ns.CreatePackage(t, setupCtx, framework.PackageOptions{Name: pkgName, Env: envName, OCI: ref})

	// Each parallel subtest needs its own context: the parent function
	// returns (and would run a deferred cancel) before parallel subtests
	// execute.
	t.Run("warm", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		fnName := "fn-oci-nd-warm-" + ns.ID
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnName, Pkg: pkgName, Entrypoint: "hello.main",
			ExecutorType: "newdeploy", MinScale: 1, MaxScale: 1,
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
		f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("Hello, newdeploy OCI!"))
	})

	t.Run("cold", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		fnName := "fn-oci-nd-cold-" + ns.ID
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnName, Pkg: pkgName, Entrypoint: "hello.main",
			ExecutorType: "newdeploy", MinScale: 0, MaxScale: 1,
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
		f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("Hello, newdeploy OCI!"))
	})
}

// TestOCIPackageNewdeployUpdate proves the package-update rollout chain works
// for OCI archives (mirrors TestNDPackageUpdate): updating the package to a
// :v2 image bumps PackageRef.ResourceVersion, which rolls the deployment and
// serves the new body. Note: mutating a tag's content WITHOUT a package
// update is deliberately not detected — pin digests in production.
func TestOCIPackageNewdeployUpdate(t *testing.T) {
	t.Parallel()
	skipOnImageVolumeLeg(t)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	hostAddr, inclusterAddr := framework.RequireRegistry(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-ocindu-" + ns.ID
	pkgName := "oci-ndu-pkg-" + ns.ID
	fnName := "fn-oci-ndu-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime,
		MinCPU: 40, MaxCPU: 80, MinMemory: 64, MaxMemory: 128,
	})

	repo := "fission-test/hello-ndu-" + ns.ID
	refV1, _ := framework.PushCodeImage(t, hostAddr, inclusterAddr, repo, "v1", pyHello("Hello, v1!"))
	refV2, _ := framework.PushCodeImage(t, hostAddr, inclusterAddr, repo, "v2", pyHello("Hello, v2!"))

	ns.CreatePackage(t, ctx, framework.PackageOptions{Name: pkgName, Env: envName, OCI: refV1})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Pkg: pkgName, Entrypoint: "hello.main",
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 1,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
	f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("Hello, v1!"))

	// Update the package to the v2 image, then touch the function so its
	// PackageRef.ResourceVersion advances (what `fn update` does) — that is
	// the signal newdeploy's updateFunction rollout chain keys on.
	ns.CLI(t, ctx, "package", "update", "--name", pkgName, "--oci", refV2)
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--pkg", pkgName, "--entrypoint", "hello.main")
	f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("Hello, v2!"))
}

// TestOCIPackageGoCompiled covers RFC-0001 Path A for a compiled language:
// a Go plugin (.so) — a binary artifact whose bytes must survive the OCI
// push/pull/extract path intact, unlike the text fixtures above. The plugin
// must be built by the env's own builder (Go plugins require an exact
// toolchain match with the runtime), so the test builds a source package
// on-cluster first, downloads the built artifact, and repackages it as an
// OCI image. The Go env loads the single file inside the extracted
// deployarchive directory.
func TestOCIPackageGoCompiled(t *testing.T) {
	t.Parallel()
	skipOnImageVolumeLeg(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	hostAddr, inclusterAddr := framework.RequireRegistry(t)
	runtime := f.Images().RequireGo(t)
	builder := f.Images().RequireGoBuilder(t)

	ns := f.NewTestNamespace(t)
	envName := "go-oci-" + ns.ID
	srcPkg := "go-oci-src-" + ns.ID
	ociPkg := "go-oci-pkg-" + ns.ID
	fnName := "fn-go-oci-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime, Builder: builder, Period: 5,
	})

	// Build the plugin on-cluster from the standard hello fixture.
	helloPath := framework.WriteTestData(t, "go/hello_world/hello.go")
	ns.CreatePackage(t, ctx, framework.PackageOptions{
		Name: srcPkg, Env: envName, Src: helloPath,
	})
	ns.WaitForPackageBuildSucceeded(t, ctx, srcPkg)

	// Download the built deploy artifact. The image must hold what an
	// EXTRACTED deploy archive holds, so a zip artifact (the Go builder
	// zips its plugin) is unwrapped into its member files; a raw artifact
	// is pushed as a single file.
	artifactPath := filepath.Join(t.TempDir(), "deploy-artifact")
	ns.CLI(t, ctx, "package", "getdeploy", "--name", srcPkg, "--output", artifactPath)
	artifact, err := os.ReadFile(artifactPath)
	require.NoError(t, err)
	require.NotEmpty(t, artifact, "built deploy artifact must not be empty")

	files := map[string]string{}
	if bytes.HasPrefix(artifact, []byte("PK\x03\x04")) {
		zr, err := zip.NewReader(bytes.NewReader(artifact), int64(len(artifact)))
		require.NoError(t, err)
		for _, zf := range zr.File {
			if zf.FileInfo().IsDir() {
				continue
			}
			rc, err := zf.Open()
			require.NoError(t, err)
			b, err := io.ReadAll(rc)
			rc.Close()
			require.NoError(t, err)
			files[zf.Name] = string(b)
		}
	} else {
		files["handler.so"] = string(artifact)
	}
	require.NotEmpty(t, files, "deploy artifact must contain at least one file")

	ref, _ := framework.PushCodeImage(t, hostAddr, inclusterAddr,
		"fission-test/go-plugin-"+ns.ID, "v1", files)

	ns.CreatePackage(t, ctx, framework.PackageOptions{Name: ociPkg, Env: envName, OCI: ref})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Pkg: ociPkg, Entrypoint: "Handler",
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("Hello"))
	assert.Contains(t, body, "Hello")
}

// requireImageVolumeLeg gates the Path B tests: FISSION_TEST_IMAGE_VOLUME is
// set only on the CI leg whose Kubernetes supports image volumes (and where
// executor.enableOCIImageVolume is on), and FISSION_TEST_REGISTRY_NODE is the
// registry address the kubelet pulls from (e.g. localhost:30500, the test
// registry's NodePort — containerd trusts localhost registries over plain
// HTTP by default).
func requireImageVolumeLeg(t *testing.T) (nodeAddr string) {
	t.Helper()
	if os.Getenv("FISSION_TEST_IMAGE_VOLUME") == "" {
		t.Skip("FISSION_TEST_IMAGE_VOLUME not set; skipping image-volume (Path B) test")
	}
	nodeAddr = os.Getenv("FISSION_TEST_REGISTRY_NODE")
	if nodeAddr == "" {
		t.Skip("FISSION_TEST_REGISTRY_NODE not set; skipping image-volume (Path B) test")
	}
	return nodeAddr
}

// TestOCIPackagePoolmgrImageVolume covers RFC-0001 Path B end-to-end: an
// eligible function on an OCI package is served from a per-image pool whose
// pods mount the code as a kubelet image volume — one container, no fetcher.
// The pod-shape assertions prove Path B actually ran rather than silently
// falling back to the fetcher path.
func TestOCIPackagePoolmgrImageVolume(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	hostAddr, _ := framework.RequireRegistry(t)
	nodeAddr := requireImageVolumeLeg(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-ocib-" + ns.ID
	pkgName := "oci-pb-pkg-" + ns.ID
	fnName := "fn-oci-pb-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime,
		MinCPU: 40, MaxCPU: 80, MinMemory: 64, MaxMemory: 128,
	})

	// The kubelet (not the fetcher) pulls this image, so the reference must
	// be node-resolvable: the registry's NodePort via localhost.
	ref, _ := framework.PushCodeImage(t, hostAddr, nodeAddr,
		"fission-test/hello-pb-"+ns.ID, "v1", pyHello("Hello, image volume!"))

	ns.CreatePackage(t, ctx, framework.PackageOptions{Name: pkgName, Env: envName, OCI: ref})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Pkg: pkgName, Entrypoint: "hello.main",
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("Hello, image volume!"))
	assert.Contains(t, body, "Hello, image volume!")

	// Prove Path B: pods of this env's per-image pool carry the image-hash
	// label, exactly one container (no fetcher), and an Image volume source.
	pods, err := f.KubeClient().CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
		LabelSelector: fv1.POOL_OCI_IMAGE_HASH + ",environmentName=" + envName,
	})
	require.NoError(t, err)
	require.NotEmpty(t, pods.Items, "per-image pool pods must exist (label %s)", fv1.POOL_OCI_IMAGE_HASH)
	for _, pod := range pods.Items {
		assert.Lenf(t, pod.Spec.Containers, 1, "pod %s: Path B pods carry no fetcher", pod.Name)
		hasImageVolume := false
		for _, v := range pod.Spec.Volumes {
			if v.Image != nil {
				hasImageVolume = true
				assert.Equal(t, ref, v.Image.Reference)
			}
		}
		assert.Truef(t, hasImageVolume, "pod %s: code must come from an image volume", pod.Name)
	}
}

// TestOCIPathBSecrets covers the RFC-0012 B-fetcher variant: a function
// with a Secret rides image-volume delivery WITH the fetcher sidecar
// retained (it materializes the secret; its exists-early-exit makes the
// fetch a no-op against the mount). Before RFC-0012 such functions fell
// back to the plain fetcher pool; the pod-shape assertions prove the new
// variant actually served it.
func TestOCIPathBSecrets(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	hostAddr, _ := framework.RequireRegistry(t)
	nodeAddr := requireImageVolumeLeg(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-ocifb-" + ns.ID
	pkgName := "oci-fb-pkg-" + ns.ID
	fnName := "fn-oci-fb-" + ns.ID
	secretName := "oci-fb-secret-" + ns.ID

	_, err := f.KubeClient().CoreV1().Secrets(ns.Name).Create(ctx, &apiv1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns.Name},
		StringData: map[string]string{"key": "value"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = f.KubeClient().CoreV1().Secrets(ns.Name).Delete(context.Background(), secretName, metav1.DeleteOptions{})
	})

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime,
		MinCPU: 40, MaxCPU: 80, MinMemory: 64, MaxMemory: 128,
	})

	// B-fetcher pods mount the image volume too, so the kubelet pulls the
	// reference: it must be node-resolvable, same as B-direct.
	ref, _ := framework.PushCodeImage(t, hostAddr, nodeAddr,
		"fission-test/hello-fb-"+ns.ID, "v1", pyHello("Hello, fallback!"))

	ns.CreatePackage(t, ctx, framework.PackageOptions{Name: pkgName, Env: envName, OCI: ref})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Pkg: pkgName, Entrypoint: "hello.main",
		Secrets: []string{secretName},
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("Hello, fallback!"))
	assert.Contains(t, body, "Hello, fallback!")

	// The serving pod must be a B-fetcher per-image pool pod: image volume
	// mounted AND the fetcher container retained for the secret.
	pods, err := f.KubeClient().CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
		LabelSelector: "functionName=" + fnName,
	})
	require.NoError(t, err)
	require.NotEmpty(t, pods.Items, "the specialized pod must exist")
	for _, pod := range pods.Items {
		assert.Containsf(t, pod.Labels, fv1.POOL_OCI_IMAGE_HASH,
			"pod %s: secrets functions ride a per-image pool since RFC-0012", pod.Name)
		assert.GreaterOrEqualf(t, len(pod.Spec.Containers), 2,
			"pod %s: B-fetcher pods keep the fetcher container", pod.Name)
		foundImageVol := false
		for _, v := range pod.Spec.Volumes {
			if v.Image != nil {
				foundImageVol = true
			}
		}
		assert.Truef(t, foundImageVol, "pod %s: B-fetcher pods mount the package image volume", pod.Name)
	}
}

// TestOCIPackageNewdeployImageVolume covers newdeploy Path B: the package
// image mounts at the fetcher's store path via a kubelet image volume — the
// fetcher container STAYS (distinguishing this from poolmgr Path B) and its
// exists-early-exit makes specialization load-only. Pod-shape assertions
// (image volume + fetcher present) plus a successful serve prove the skip
// without fragile log grepping.
func TestOCIPackageNewdeployImageVolume(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	hostAddr, _ := framework.RequireRegistry(t)
	nodeAddr := requireImageVolumeLeg(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-ndiv-" + ns.ID
	pkgName := "oci-ndiv-pkg-" + ns.ID
	fnName := "fn-oci-ndiv-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime,
		MinCPU: 40, MaxCPU: 80, MinMemory: 64, MaxMemory: 128,
	})

	// The kubelet pulls this image (node-resolvable NodePort address).
	ref, _ := framework.PushCodeImage(t, hostAddr, nodeAddr,
		"fission-test/hello-ndiv-"+ns.ID, "v1", pyHello("Hello, newdeploy image volume!"))

	ns.CreatePackage(t, ctx, framework.PackageOptions{Name: pkgName, Env: envName, OCI: ref})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Pkg: pkgName, Entrypoint: "hello.main",
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 1,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("Hello, newdeploy image volume!"))

	pods, err := f.KubeClient().CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
		LabelSelector: "functionName=" + fnName,
	})
	require.NoError(t, err)
	require.NotEmpty(t, pods.Items, "newdeploy function pods must exist")
	for _, pod := range pods.Items {
		hasFetcher := false
		for _, c := range pod.Spec.Containers {
			if c.Name == "fetcher" {
				hasFetcher = true
			}
		}
		assert.Truef(t, hasFetcher, "pod %s: newdeploy Path B keeps the fetcher container", pod.Name)
		hasImageVolume := false
		for _, v := range pod.Spec.Volumes {
			if v.Image != nil {
				hasImageVolume = true
				assert.Equal(t, ref, v.Image.Reference)
			}
		}
		assert.Truef(t, hasImageVolume, "pod %s: code must come from an image volume", pod.Name)
	}
}

// TestOCIProducerBuild covers RFC-0012 phase 4 end-to-end on a live cluster:
// with the buildermgr's packageRegistry configured (the FISSION_TEST_OCI_PRODUCER
// leg), a plain `fission fn create --src` source build publishes the
// deployment archive as a digest-pinned OCI image, the Package is rewritten
// to Archive{Type: oci} with the OCIPublished condition, and the function
// serves through the image-volume path — zero per-package opt-in.
func TestOCIProducerBuild(t *testing.T) {
	t.Parallel()
	if os.Getenv("FISSION_TEST_OCI_PRODUCER") == "" {
		t.Skip("FISSION_TEST_OCI_PRODUCER not set; skipping OCI producer test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	pyImage := f.Images().RequirePython(t)
	builderImage := f.Images().RequirePythonBuilder(t)

	ns := f.NewTestNamespace(t)

	envName := "py-ociprod-" + ns.ID
	fnName := "fn-ociprod-" + ns.ID
	routePath := "/" + fnName

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name:    envName,
		Image:   pyImage,
		Builder: builderImage,
	})
	ns.WaitForBuilderReady(t, ctx, envName)

	srcZip := framework.ZipTestDataDir(t, "python/sourcepkg", "demo-src-pkg-ociprod.zip")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name:       fnName,
		Env:        envName,
		Src:        srcZip,
		Entrypoint: "user.main",
		BuildCmd:   "./build.sh",
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{
		Function: fnName,
		URL:      routePath,
		Method:   "GET",
	})

	pkgName := ns.FunctionPackageName(t, ctx, fnName)
	ns.WaitForPackageBuildSucceeded(t, ctx, pkgName)

	// The producer rewrote the package to a digest-pinned OCI archive.
	pkg, err := f.FissionClient().CoreV1().Packages(ns.Name).Get(ctx, pkgName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, fv1.ArchiveTypeOCI, pkg.Spec.Deployment.Type,
		"a registry-enabled build must publish an OCI archive; build log:\n%s", pkg.Status.BuildLog)
	require.NotNil(t, pkg.Spec.Deployment.OCI)
	assert.Contains(t, pkg.Spec.Deployment.OCI.Digest, "sha256:", "digest-pinned by default")
	if nodeReg := os.Getenv("FISSION_TEST_REGISTRY_NODE"); nodeReg != "" {
		assert.Contains(t, pkg.Spec.Deployment.OCI.Image, nodeReg,
			"the recorded reference must carry the published (node-resolvable) prefix")
	}
	cond := meta.FindStatusCondition(pkg.Status.Conditions, fv1.PackageConditionOCIPublished)
	require.NotNil(t, cond, "the publish outcome must be observable")
	assert.Equal(t, metav1.ConditionTrue, cond.Status)

	// And the built function actually serves (image-volume delivery on this
	// leg; the executor's gate handles the rest).
	body := f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("a: 1"))
	assert.Contains(t, body, "a: 1")
}
