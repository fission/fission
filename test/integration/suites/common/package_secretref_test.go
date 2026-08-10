// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// authServerScript is a minimal authenticated file server: it serves the zip
// mounted at /data/archive.zip only when the request carries either the
// expected Authorization value (WANT_AUTH) or the expected X-Api-Key header
// (WANT_KEY); everything else gets a 401. It runs on the node runtime image,
// whose entrypoint we override — no extra test image needed.
const authServerScript = `
const http = require('http'), fs = require('fs');
const zip = fs.readFileSync('/data/archive.zip');
const wantAuth = process.env.WANT_AUTH || '', wantKey = process.env.WANT_KEY || '';
http.createServer((req, res) => {
  const okAuth = wantAuth !== '' && req.headers['authorization'] === wantAuth;
  const okKey = wantKey !== '' && req.headers['x-api-key'] === wantKey;
  if (okAuth || okKey) { res.writeHead(200, {'Content-Type': 'application/zip'}); res.end(zip); }
  else { res.writeHead(401); res.end('unauthorized'); }
}).listen(8000, () => console.log('authsrv up'));
`

// startAuthServer runs the authenticated zip server in the test namespace and
// returns the in-cluster URL of the archive. The server is a plain Deployment
// + Service on the node runtime image; the zip travels in a ConfigMap
// (binaryData), so no image build or external host is involved. It waits for
// the Deployment to have a ready replica so the very first package fetch (or
// build) cannot race the server's startup.
func startAuthServer(t *testing.T, ctx context.Context, f *framework.Framework, nsName, name, nodeImage, zipPath, wantAuth, wantKey string) string {
	t.Helper()
	kube := f.KubeClient()

	zipBytes, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	require.Less(t, len(zipBytes), 900*1024, "archive must fit in a ConfigMap")

	_, err = kube.CoreV1().ConfigMaps(nsName).Create(ctx, &apiv1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		BinaryData: map[string][]byte{"archive.zip": zipBytes},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	labels := map[string]string{"app": name}
	one := int32(1)
	_, err = kube.AppsV1().Deployments(nsName).Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: apiv1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: apiv1.PodSpec{
					Containers: []apiv1.Container{{
						Name:    "authsrv",
						Image:   nodeImage,
						Command: []string{"node", "-e", authServerScript},
						Env: []apiv1.EnvVar{
							{Name: "WANT_AUTH", Value: wantAuth},
							{Name: "WANT_KEY", Value: wantKey},
						},
						Ports: []apiv1.ContainerPort{{ContainerPort: 8000}},
						VolumeMounts: []apiv1.VolumeMount{
							{Name: "archive", MountPath: "/data", ReadOnly: true},
						},
						ReadinessProbe: &apiv1.Probe{
							ProbeHandler: apiv1.ProbeHandler{
								TCPSocket: &apiv1.TCPSocketAction{
									Port: intstr.FromInt32(8000),
								},
							},
							PeriodSeconds: 1,
						},
					}},
					Volumes: []apiv1.Volume{{
						Name: "archive",
						VolumeSource: apiv1.VolumeSource{
							ConfigMap: &apiv1.ConfigMapVolumeSource{
								LocalObjectReference: apiv1.LocalObjectReference{Name: name},
							},
						},
					}},
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = kube.CoreV1().Services(nsName).Create(ctx, &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apiv1.ServiceSpec{
			Selector: labels,
			Ports:    []apiv1.ServicePort{{Port: 8000, TargetPort: intstr.FromInt32(8000)}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		d, err := kube.AppsV1().Deployments(nsName).Get(ctx, name, metav1.GetOptions{})
		return err == nil && d.Status.ReadyReplicas >= 1
	}, 3*time.Minute, 2*time.Second, "auth server must become ready before packages fetch from it")

	return fmt.Sprintf("http://%s.%s.svc.cluster.local:8000/archive.zip", name, nsName)
}

func basicAuthHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// TestPackageSecretRefDeployArchive covers the function-pod fetch path of
// RFC-0031: a deploy archive on an authenticated HTTP server, credentials in
// a Secret in the fetching pod's namespace, referenced via --deploysecret.
// Variant 1 uses basic auth (username/password keys); variant 2 uses an
// arbitrary header (headers key, the X-JFrog-Art-Api shape from #3207).
func TestPackageSecretRefDeployArchive(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	nodeImage := f.Images().RequireNode(t)
	ns := f.NewTestNamespace(t)

	zipPath := framework.ZipTestDataDir(t, "nodejs/hello", "hello-deploy.zip")
	archiveURL := startAuthServer(t, ctx, f, ns.Name, "authsrv-"+ns.ID, nodeImage,
		zipPath, basicAuthHeader("fission", "s3cret!"), "key-"+ns.ID)

	envName := "node-auth-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: nodeImage})

	// The credential Secrets live in the namespace the function pods run in
	// (the test namespace) — that is where the fetcher resolves them.
	kube := f.KubeClient()
	_, err := kube.CoreV1().Secrets(ns.Name).Create(ctx, &apiv1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "basic-creds"},
		StringData: map[string]string{"username": "fission", "password": "s3cret!"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kube.CoreV1().Secrets(ns.Name).Create(ctx, &apiv1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "header-creds"},
		StringData: map[string]string{"headers": "X-Api-Key: key-" + ns.ID},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Run("basic_auth", func(t *testing.T) {
		pkgName := "auth-basic-pkg-" + ns.ID
		fnName := "auth-basic-fn-" + ns.ID
		ns.CreatePackage(t, ctx, framework.PackageOptions{
			Name: pkgName, Env: envName, Deploy: archiveURL, DeploySecret: "basic-creds",
		})
		pkg := ns.GetPackage(t, ctx, pkgName)
		require.NotNil(t, pkg.Spec.Deployment.SecretRef, "the CLI must record the SecretRef")
		assert.Empty(t, pkg.Spec.Deployment.Checksum.Sum, "no anonymous checksum download for credentialed URLs")

		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Pkg: pkgName, Entrypoint: "hello"})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
		body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("hello, world!"))
		assert.Contains(t, body, "hello, world!")
	})

	t.Run("header_auth", func(t *testing.T) {
		pkgName := "auth-header-pkg-" + ns.ID
		fnName := "auth-header-fn-" + ns.ID
		ns.CreatePackage(t, ctx, framework.PackageOptions{
			Name: pkgName, Env: envName, Deploy: archiveURL, DeploySecret: "header-creds",
		})
		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Pkg: pkgName, Entrypoint: "hello"})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
		body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("hello, world!"))
		assert.Contains(t, body, "hello, world!")
	})
}

// TestPackageSecretRefSourceBuild covers the builder-pod fetch path of
// RFC-0031: the source archive sits behind authentication and the builder-pod
// fetcher must present the credential from --srcsecret. The wrong-credential
// case pins the failure mode: a failed build with the HTTP error surfaced in
// the build log, never a silent anonymous fetch.
func TestPackageSecretRefSourceBuild(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	nodeImage := f.Images().RequireNode(t)
	pyImage := f.Images().RequirePython(t)
	builderImage := f.Images().RequirePythonBuilder(t)
	ns := f.NewTestNamespace(t)

	srcZip := framework.ZipTestDataDir(t, "python/sourcepkg", "auth-src-pkg.zip")
	archiveURL := startAuthServer(t, ctx, f, ns.Name, "authsrv-"+ns.ID, nodeImage,
		srcZip, basicAuthHeader("builder", "bldpass"), "")

	envName := "py-auth-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: pyImage, Builder: builderImage})
	ns.WaitForBuilderReady(t, ctx, envName)

	kube := f.KubeClient()
	_, err := kube.CoreV1().Secrets(ns.Name).Create(ctx, &apiv1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "src-creds"},
		StringData: map[string]string{"username": "builder", "password": "bldpass"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kube.CoreV1().Secrets(ns.Name).Create(ctx, &apiv1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-creds"},
		StringData: map[string]string{"username": "builder", "password": "nope"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Run("build_succeeds_with_credentials", func(t *testing.T) {
		pkgName := "auth-src-pkg-" + ns.ID
		fnName := "auth-src-fn-" + ns.ID
		ns.CreatePackage(t, ctx, framework.PackageOptions{
			Name: pkgName, Env: envName, Src: archiveURL, SrcSecret: "src-creds", BuildCmd: "./build.sh",
		})
		ns.WaitForPackageBuildSucceeded(t, ctx, pkgName)

		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Pkg: pkgName, Entrypoint: "user.main"})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
		body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("a: 1"))
		assert.Contains(t, body, "a: 1")
	})

	t.Run("build_fails_visibly_with_wrong_credentials", func(t *testing.T) {
		pkgName := "auth-bad-pkg-" + ns.ID
		ns.CreatePackage(t, ctx, framework.PackageOptions{
			Name: pkgName, Env: envName, Src: archiveURL, SrcSecret: "bad-creds", BuildCmd: "./build.sh",
		})
		ns.WaitForPackageBuildStatus(t, ctx, pkgName, fv1.BuildStatusFailed, 5*time.Minute)
		pkg := ns.GetPackage(t, ctx, pkgName)
		assert.Contains(t, pkg.Status.BuildLog, "401",
			"the HTTP auth failure must be visible in the build log, build log:\n%s", pkg.Status.BuildLog)
	})
}
