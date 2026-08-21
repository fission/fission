// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/versioning"
)

func describeFunction() *fv1.Function {
	return &fv1.Function{
		Name: "hello", Namespace: "default",
		Labels: map[string]string{"team": "core"},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{Name: "nodejs", Namespace: "default"},
			Package:     fv1.FunctionPackageRef{PackageRef: fv1.PackageRef{Name: "hello-pkg", Namespace: "default"}},
			InvokeStrategy: fv1.InvokeStrategy{
				ExecutionStrategy: fv1.ExecutionStrategy{ExecutorType: fv1.ExecutorTypePoolmgr},
			},
		},
		Status: fv1.FunctionStatus{Conditions: []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "FunctionReady", Message: "function is ready"},
		}},
	}
}

func describePackage(buildStatus fv1.BuildStatus, buildLog string) *fv1.Package {
	return &fv1.Package{
		Name: "hello-pkg", Namespace: "default",
		Status: fv1.PackageStatus{BuildStatus: buildStatus, BuildLog: buildLog},
	}
}

func describeFunctionPod() *corev1.Pod {
	return &corev1.Pod{
		Name: "poolmgr-nodejs-hello-abc", Namespace: "fission-function",
		Labels: map[string]string{
			fv1.FUNCTION_NAME:      "hello",
			fv1.FUNCTION_NAMESPACE: "default",
			fv1.EXECUTOR_TYPE:      "poolmgr",
			fv1.MANAGED:            "false",
			fv1.SERVED_LABEL:       fv1.SERVED_VALUE,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning, PodIP: "10.0.0.5",
			ContainerStatuses: []corev1.ContainerStatus{{Ready: true}, {Ready: true}},
		},
	}
}

func setDescribeClients(t *testing.T, fissionObjs []runtime.Object, pods ...runtime.Object) {
	t.Helper()
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(fissionObjs...),
		KubernetesClient: k8sfake.NewClientset(pods...),
		Namespace:        "default",
	})
}

func describeInput(name, ns string) dummy.Cli {
	in := dummy.TestFlagSet()
	in.SetString(flagkey.FnName, name)
	in.SetString(flagkey.Namespace, ns)
	return in
}

func TestFunctionDescribe(t *testing.T) {
	t.Run("renders summary, conditions, package build status, and pods", func(t *testing.T) {
		fn := describeFunction()
		pkg := describePackage(fv1.BuildStatusSucceeded, "")
		// A second pod with 1 of 2 containers ready, to lock the READY column as
		// ready/total (a total/ready regression would render "2/1").
		partial := describeFunctionPod()
		partial.Name = "poolmgr-nodejs-hello-def"
		partial.Status.ContainerStatuses = []corev1.ContainerStatus{{Ready: true}, {Ready: false}}
		setDescribeClients(t, []runtime.Object{fn, pkg}, describeFunctionPod(), partial)

		out := captureStdout(t, func() error { return Describe(describeInput("hello", "default")) })

		assert.Contains(t, out, "hello", "function name")
		assert.Contains(t, out, "nodejs", "environment")
		assert.Contains(t, out, "poolmgr", "executor type")
		assert.Contains(t, out, "Ready", "function condition")
		assert.Contains(t, out, "succeeded", "package build status")
		assert.Contains(t, out, "poolmgr-nodejs-hello-abc", "pod name")
		assert.Contains(t, out, "2/2", "fully-ready pod count")
		assert.Contains(t, out, "1/2", "partially-ready pod count (ready/total order)")
		assert.Contains(t, out, "Invocable", "invocability headline")
		assert.Contains(t, out, "Yes", "ready function with a warm pod is invocable")
		assert.Contains(t, out, "serving", "served pod surfaced in the invocability headline (RFC-0002 data plane)")
		assert.Contains(t, out, "SERVED", "pods table has a SERVED column")
		// RFC-0025: an unversioned function with no orphaned versions/aliases
		// renders the VERSIONING section as a single "disabled" line -- this
		// is the only change this feature makes to the pre-existing describe
		// output, so pin it explicitly (byte-stability for everything above).
		assert.Contains(t, out, "\nVERSIONING:\n  Versioning: disabled\n", "unversioned function's VERSIONING section is a single disabled line")
	})

	t.Run("a not-Ready function reports not invocable", func(t *testing.T) {
		fn := describeFunction()
		fn.Status.Conditions = []metav1.Condition{
			{Type: fv1.FunctionConditionReady, Status: metav1.ConditionFalse, Reason: "PackageFailed", Message: "build failed"},
		}
		pkg := describePackage(fv1.BuildStatusFailed, "")
		setDescribeClients(t, []runtime.Object{fn, pkg})

		out := captureStdout(t, func() error { return Describe(describeInput("hello", "default")) })

		assert.Contains(t, out, "Invocable", "invocability headline")
		assert.Contains(t, out, "No", "not-Ready function is not invocable")
	})

	t.Run("a failed build shows the build log", func(t *testing.T) {
		fn := describeFunction()
		pkg := describePackage(fv1.BuildStatusFailed, `npm ERR! missing script: build`)
		setDescribeClients(t, []runtime.Object{fn, pkg})

		out := captureStdout(t, func() error { return Describe(describeInput("hello", "default")) })

		assert.Contains(t, out, "failed", "build status")
		assert.Contains(t, out, "npm ERR! missing script: build", "build log on failure")
	})

	t.Run("a missing package degrades gracefully, not an error", func(t *testing.T) {
		fn := describeFunction() // references hello-pkg, which is absent from the clientset
		setDescribeClients(t, []runtime.Object{fn})

		out := captureStdout(t, func() error { return Describe(describeInput("hello", "default")) })

		assert.Contains(t, out, "hello", "still renders the function summary")
		assert.NotContains(t, out, "succeeded")
	})

	t.Run("a missing function is an error", func(t *testing.T) {
		setDescribeClients(t, []runtime.Object{describeFunction()})
		err := Describe(describeInput("does-not-exist", "default"))
		require.Error(t, err)
	})

	t.Run("versioning enabled renders mode/retain, version count, and the alias table", func(t *testing.T) {
		fn := describeFunction()
		fn.Spec.Versioning = &fv1.VersioningConfig{Mode: fv1.VersioningModeAuto, Retain: new(5)}
		version := describeVersionObj("hello-v1", "hello", 1)
		weight := 100
		alias := &fv1.FunctionAlias{
			Name: "prod", Namespace: "default",
			Spec: fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1", Weight: &weight},
			Status: fv1.FunctionAliasStatus{
				ResolvedVersion: "hello-v1",
				Conditions: []metav1.Condition{
					{Type: fv1.FunctionAliasConditionEnvDrift, Status: metav1.ConditionTrue, Reason: fv1.FunctionAliasReasonEnvGenerationDrift},
				},
			},
		}
		setDescribeClients(t, []runtime.Object{fn, version, alias})

		out := captureStdout(t, func() error { return Describe(describeInput("hello", "default")) })

		assert.Contains(t, out, "mode=auto retain=5")
		assertFieldMatches(t, out, "Versions:", "1", "one version listed")
		assert.Contains(t, out, "NAME")
		assert.Contains(t, out, "TARGET")
		assert.Contains(t, out, "WEIGHT")
		assert.Contains(t, out, "ENVDRIFT", "alias mini-table header")
		assert.Contains(t, out, "prod")
		assert.Contains(t, out, "hello-v1", "alias TARGET is the effective target")
		assert.Contains(t, out, "100", "alias weight")
		assert.Contains(t, out, "True", "alias EnvDrift condition status")
	})

	t.Run("versioning enabled with no aliases yet shows the none-value alias table", func(t *testing.T) {
		fn := describeFunction()
		fn.Spec.Versioning = &fv1.VersioningConfig{Mode: fv1.VersioningModeManual}
		setDescribeClients(t, []runtime.Object{fn})

		out := captureStdout(t, func() error { return Describe(describeInput("hello", "default")) })

		assert.Contains(t, out, "mode=manual")
		assertFieldMatches(t, out, "Versions:", "0", "no versions minted yet")
	})

	t.Run("versioning enabled degrades gracefully when versions/aliases List fails", func(t *testing.T) {
		fn := describeFunction()
		fn.Spec.Versioning = &fv1.VersioningConfig{Mode: fv1.VersioningModeAuto}
		fc := fissionfake.NewClientset(fn)
		fc.PrependReactor("list", "functionversions", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, assert.AnError
		})
		fc.PrependReactor("list", "functionaliases", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, assert.AnError
		})
		cmd.ResetClientsetForTest()
		cmd.SetClientset(cmd.Client{FissionClientSet: fc, KubernetesClient: k8sfake.NewClientset(), Namespace: "default"})

		out := captureStdout(t, func() error { return Describe(describeInput("hello", "default")) })

		assert.Contains(t, out, "hello", "still renders the function summary")
		assert.Contains(t, out, "mode=auto")
		assertFieldMatches(t, out, "Versions:", "0", "a failed List degrades the count to 0 rather than erroring the view")
	})
}

// assertFieldMatches asserts that out contains a tabwriter-rendered
// "label<whitespace>value" line -- the describe/getmeta printers write
// fields through a text/tabwriter, which right-pads with spaces to align
// columns rather than emitting the literal tab byte, so a plain
// assert.Contains(out, "Label:\tvalue\n") never matches real output.
func assertFieldMatches(t *testing.T, out, label, value, msgAndArgs string) {
	t.Helper()
	pattern := regexp.MustCompile(regexp.QuoteMeta(label) + `\s+` + regexp.QuoteMeta(value) + `\s*\n`)
	assert.True(t, pattern.MatchString(out), fmt.Sprintf("%s: expected %q to match %s in:\n%s", msgAndArgs, value, pattern, out))
}

// describeVersionObj builds a minimal FunctionVersion for describe tests.
func describeVersionObj(name, fnName string, sequence int64) *fv1.FunctionVersion {
	return &fv1.FunctionVersion{
		Name:      name,
		Namespace: "default",
		Labels:    map[string]string{fv1.VersionFunctionNameLabel: fnName},
		Spec: fv1.FunctionVersionSpec{
			FunctionName: fnName,
			Sequence:     sequence,
			Snapshot: fv1.FunctionSpec{
				Environment: fv1.EnvironmentReference{Name: "nodejs", Namespace: "default"},
				Package:     fv1.FunctionPackageRef{FunctionName: "handler"},
			},
			PackageDigest:         "sha256:abc123",
			EnvObservedGeneration: 1,
			EnvRuntimeImage:       "fission/node-env:v1",
			PublishedAt:           metav1.NewTime(time.Now()),
		},
	}
}

func describeVersionInput(name, namespace, version string) dummy.Cli {
	in := describeInput(name, namespace)
	in.SetString(flagkey.FnDescribeVersion, version)
	return in
}

func TestFunctionDescribeVersion(t *testing.T) {
	t.Run("renders the SNAPSHOT inspector: sequence, digest, description, entrypoint, env, and current drift", func(t *testing.T) {
		fn := describeFunction()
		version := describeVersionObj("hello-v1", "hello", 1)
		version.Annotations = map[string]string{versioning.DescriptionAnnotation: "fixed the widget bug"}
		env := &fv1.Environment{
			Name: "nodejs", Namespace: "default", Generation: 1,
		}
		setDescribeClients(t, []runtime.Object{fn, version, env})

		out := captureStdout(t, func() error { return Describe(describeVersionInput("hello", "default", "hello-v1")) })

		assertFieldMatches(t, out, "Name:", "hello-v1", "version name")
		assertFieldMatches(t, out, "Sequence:", "1", "sequence")
		assert.Contains(t, out, "sha256:abc123", "digest")
		assert.Contains(t, out, "fixed the widget bug", "description annotation")
		assert.Contains(t, out, "handler", "snapshot entrypoint")
		assert.Contains(t, out, "nodejs", "snapshot environment name")
		assert.Contains(t, out, "fission/node-env:v1", "env runtime image observation")
		assert.Contains(t, out, "current", "env generation matches the live environment: no drift")
		assert.NotContains(t, out, "DRIFT")
		assert.Contains(t, out, "\nALIASED-BY:\n", "aliased-by section present")
	})

	t.Run("no description annotation renders '-'", func(t *testing.T) {
		fn := describeFunction()
		version := describeVersionObj("hello-v1", "hello", 1)
		setDescribeClients(t, []runtime.Object{fn, version})

		out := captureStdout(t, func() error { return Describe(describeVersionInput("hello", "default", "hello-v1")) })

		assertFieldMatches(t, out, "Description:", "-", "unset description annotation")
	})

	t.Run("marks DRIFT when the live environment generation has moved past the snapshot observation", func(t *testing.T) {
		fn := describeFunction()
		version := describeVersionObj("hello-v1", "hello", 1) // EnvObservedGeneration: 1
		env := &fv1.Environment{
			Name: "nodejs", Namespace: "default", Generation: 2,
		}
		setDescribeClients(t, []runtime.Object{fn, version, env})

		out := captureStdout(t, func() error { return Describe(describeVersionInput("hello", "default", "hello-v1")) })

		assert.Contains(t, out, "DRIFT")
		assert.Contains(t, out, "live generation=2")
	})

	t.Run("an unreadable live environment renders drift as not assessable, not an error", func(t *testing.T) {
		fn := describeFunction()
		version := describeVersionObj("hello-v1", "hello", 1) // references "nodejs", which is absent
		setDescribeClients(t, []runtime.Object{fn, version})

		out := captureStdout(t, func() error { return Describe(describeVersionInput("hello", "default", "hello-v1")) })

		assertFieldMatches(t, out, "Env Drift:", "<none>", "unreadable live environment: drift not assessable")
	})

	t.Run("lists ALIASED-BY aliases whose effective target is this version, excluding others", func(t *testing.T) {
		fn := describeFunction()
		v1obj := describeVersionObj("hello-v1", "hello", 1)
		v2obj := describeVersionObj("hello-v2", "hello", 2)
		aliasProd := &fv1.FunctionAlias{
			Name: "prod", Namespace: "default",
			Spec: fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
		}
		// Digest-pinned, resolved via Status rather than Spec.Version -- still
		// counts as targeting hello-v1 (fv1.FunctionAlias.EffectiveTarget
		// prefers Spec.Version, falls back to Status.ResolvedVersion).
		aliasGitops := &fv1.FunctionAlias{
			Name: "gitops", Namespace: "default",
			Spec:   fv1.FunctionAliasSpec{FunctionName: "hello", PackageDigest: "sha256:" + strings40('a')},
			Status: fv1.FunctionAliasStatus{ResolvedVersion: "hello-v1"},
		}
		aliasCanary := &fv1.FunctionAlias{
			Name: "canary", Namespace: "default",
			Spec: fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v2"},
		}
		setDescribeClients(t, []runtime.Object{fn, v1obj, v2obj, aliasProd, aliasGitops, aliasCanary})

		out := captureStdout(t, func() error { return Describe(describeVersionInput("hello", "default", "hello-v1")) })

		assert.Contains(t, out, "prod")
		assert.Contains(t, out, "gitops")
		assert.NotContains(t, out, "canary", "an alias targeting a different version must not be listed in ALIASED-BY")
	})

	t.Run("no aliases target this version shows the none-value table", func(t *testing.T) {
		fn := describeFunction()
		version := describeVersionObj("hello-v1", "hello", 1)
		setDescribeClients(t, []runtime.Object{fn, version})

		out := captureStdout(t, func() error { return Describe(describeVersionInput("hello", "default", "hello-v1")) })

		assert.Contains(t, out, "\nALIASED-BY:\n  <none>\n")
	})

	t.Run("preflight: a missing version is an error naming the function", func(t *testing.T) {
		fn := describeFunction()
		setDescribeClients(t, []runtime.Object{fn})

		err := Describe(describeVersionInput("hello", "default", "does-not-exist"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does-not-exist")
	})

	t.Run("preflight: a version belonging to a different function is an error", func(t *testing.T) {
		fn := describeFunction()
		other := describeVersionObj("other-v1", "other-function", 1)
		setDescribeClients(t, []runtime.Object{fn, other})

		err := Describe(describeVersionInput("hello", "default", "other-v1"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "other-function")
	})
}

// strings40 repeats r 64 times, enough for a well-formed sha256 hex digest
// suffix in tests that don't care about its actual value.
func strings40(r rune) string {
	out := make([]rune, 64)
	for i := range out {
		out[i] = r
	}
	return string(out)
}
