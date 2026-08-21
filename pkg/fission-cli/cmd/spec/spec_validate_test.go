// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec/types"
)

// fakeInput satisfies cli.Input by embedding it; only Context is exercised by Validate.
type fakeInput struct {
	cli.Input
	ctx context.Context
}

func (f fakeInput) Context() context.Context { return f.ctx }

func TestCrdToYaml(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		resource Resource
		wantKind string
		wantName string
	}{
		{"Package", &fv1.Package{Name: "pkg"}, "Package", "pkg"},
		{"Function", &fv1.Function{Name: "fn"}, "Function", "fn"},
		{"Environment", &fv1.Environment{Name: "env"}, "Environment", "env"},
		{"HTTPTrigger", &fv1.HTTPTrigger{Name: "ht"}, "HTTPTrigger", "ht"},
		{"KubernetesWatchTrigger", &fv1.KubernetesWatchTrigger{Name: "kw"}, "KubernetesWatchTrigger", "kw"},
		{"MessageQueueTrigger", &fv1.MessageQueueTrigger{Name: "mqt"}, "MessageQueueTrigger", "mqt"},
		{"TimeTrigger", &fv1.TimeTrigger{Name: "tt"}, "TimeTrigger", "tt"},
		{"Workflow", &fv1.Workflow{Name: "wf"}, "Workflow", "wf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			meta, kind, data, err := crdToYaml(tt.resource)
			require.NoError(t, err)
			assert.Equal(t, tt.wantKind, kind)
			assert.Equal(t, tt.wantName, meta.Name)
			assert.Contains(t, string(data), "kind: "+tt.wantKind)
			assert.Contains(t, string(data), "apiVersion: "+fv1.CRD_VERSION)
			// The caller's object is not stamped: crdToYaml works on a copy.
			assert.Empty(t, tt.resource.GetObjectKind().GroupVersionKind().Kind)
		})
	}

	t.Run("ArchiveUploadSpec", func(t *testing.T) {
		t.Parallel()
		meta, kind, data, err := archiveUploadSpecToYaml(types.ArchiveUploadSpec{Name: "ar"})
		require.NoError(t, err)
		assert.Equal(t, "ArchiveUploadSpec", kind)
		assert.Equal(t, "ar", meta.Name)
		assert.Contains(t, string(data), "kind: ArchiveUploadSpec")
	})

	t.Run("type outside the scheme errors", func(t *testing.T) {
		t.Parallel()
		_, _, _, err := crdToYaml(&apiv1.ConfigMap{Name: "cm"})
		require.Error(t, err)
	})
}

func TestValidateFunctionReference(t *testing.T) {
	t.Parallel()
	fr := newFissionResources()

	t.Run("known function marks it referenced", func(t *testing.T) {
		functions := map[string]bool{"default/hello": false}
		err := fr.validateFunctionReference(functions, "HTTPTrigger",
			&metav1.ObjectMeta{Name: "ht", Namespace: "default"},
			fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "hello"})
		require.NoError(t, err)
		assert.True(t, functions["default/hello"])
	})

	t.Run("unknown function errors", func(t *testing.T) {
		err := fr.validateFunctionReference(map[string]bool{}, "HTTPTrigger",
			&metav1.ObjectMeta{Name: "ht", Namespace: "default"},
			fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "ghost"})
		require.ErrorContains(t, err, "references unknown function")
	})

	t.Run("non-name reference type is skipped", func(t *testing.T) {
		err := fr.validateFunctionReference(map[string]bool{}, "HTTPTrigger",
			&metav1.ObjectMeta{Name: "ht", Namespace: "default"},
			fv1.FunctionReference{Type: "selector"})
		require.NoError(t, err)
	})
}

func poolmgrFunction(name, pkgName, pkgNS string) fv1.Function {
	fn := fv1.Function{Name: name, Namespace: "default"}
	fn.Spec.Environment = fv1.EnvironmentReference{Name: "env", Namespace: "default"}
	fn.Spec.Package.PackageRef = fv1.PackageRef{Name: pkgName, Namespace: pkgNS}
	fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorTypePoolmgr
	fn.Spec.FunctionTimeout = 60
	return fn
}

func validateWith(t *testing.T, fr *FissionResources) ([]string, error) {
	t.Helper()
	client := cmd.Client{KubernetesClient: k8sfake.NewClientset()}
	return fr.Validate(fakeInput{ctx: t.Context()}, client)
}

func TestValidate(t *testing.T) {
	t.Run("package references an unknown archive", func(t *testing.T) {
		fr := newFissionResources()
		pkg := fv1.Package{Name: "pkg", Namespace: "default"}
		pkg.Spec.Source.URL = ARCHIVE_URL_PREFIX + "missing"
		fr.Packages = []fv1.Package{pkg}
		_, err := validateWith(t, fr)
		require.ErrorContains(t, err, "references unknown")
	})

	t.Run("unreferenced archive errors", func(t *testing.T) {
		fr := newFissionResources()
		fr.ArchiveUploadSpecs = []types.ArchiveUploadSpec{{Name: "orphan"}}
		_, err := validateWith(t, fr)
		require.ErrorContains(t, err, "is not used in any package")
	})

	t.Run("function references an unknown package", func(t *testing.T) {
		fr := newFissionResources()
		fr.Functions = []fv1.Function{poolmgrFunction("fn", "missing", "default")}
		_, err := validateWith(t, fr)
		require.ErrorContains(t, err, "references unknown package")
	})

	t.Run("function references a package in another namespace", func(t *testing.T) {
		fr := newFissionResources()
		fr.Functions = []fv1.Function{poolmgrFunction("fn", "pkg", "other-ns")}
		_, err := validateWith(t, fr)
		require.ErrorContains(t, err, "outside of its namespace")
	})

	t.Run("trigger references an unknown function", func(t *testing.T) {
		fr := newFissionResources()
		ht := fv1.HTTPTrigger{Name: "ht", Namespace: "default"}
		ht.Kind = "HTTPTrigger"
		ht.Spec.FunctionReference = fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "ghost"}
		fr.HttpTriggers = []fv1.HTTPTrigger{ht}
		_, err := validateWith(t, fr)
		require.ErrorContains(t, err, "references unknown function")
	})

	t.Run("container executor skips the package reference check", func(t *testing.T) {
		fr := newFissionResources()
		fn := fv1.Function{Name: "cfn", Namespace: "default"}
		fn.Spec.Environment = fv1.EnvironmentReference{Name: "env", Namespace: "default"}
		fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorTypeContainer
		fn.Spec.FunctionTimeout = 60
		fr.Functions = []fv1.Function{fn}
		_, err := validateWith(t, fr)
		if err != nil {
			assert.NotContains(t, err.Error(), "references unknown package")
		}
	})

	t.Run("configmap not present in cluster warns", func(t *testing.T) {
		fr := newFissionResources()
		fn := poolmgrFunction("fn", "pkg", "default")
		fn.Spec.ConfigMaps = []fv1.ConfigMapReference{{Name: "cfg", Namespace: "default"}}
		pkg := fv1.Package{Name: "pkg", Namespace: "default"}
		fr.Functions = []fv1.Function{fn}
		fr.Packages = []fv1.Package{pkg}
		warnings, _ := validateWith(t, fr)
		assert.Contains(t, warnings, "Configmap cfg is referred in the spec but not present in the cluster")
	})

	t.Run("function referencing an undeclared environment warns", func(t *testing.T) {
		fr := newFissionResources()
		fn := poolmgrFunction("fn", "pkg", "default")
		fr.Functions = []fv1.Function{fn}
		fr.Packages = []fv1.Package{{Name: "pkg", Namespace: "default"}}
		warnings, _ := validateWith(t, fr)
		assert.Contains(t, warnings, "Environment env is referenced in function fn but not declared in specs")
	})

	t.Run("FunctionAlias references an unknown function warns, not errors", func(t *testing.T) {
		fr := newFissionResources()
		fr.FunctionAliases = []fv1.FunctionAlias{{
			Name: "prod", Namespace: "default",
			Spec: fv1.FunctionAliasSpec{FunctionName: "ghost", Version: "ghost-v1"},
		}}
		warnings, err := validateWith(t, fr)
		require.NoError(t, err, "a dangling alias->function ref is informational (eventual consistency), not a hard error")
		require.Len(t, warnings, 1)
		assert.Contains(t, warnings[0], "FunctionAlias 'prod' references unknown function 'ghost' in the spec set")
	})

	t.Run("FunctionAlias with invalid spec errors", func(t *testing.T) {
		fr := newFissionResources()
		fr.Functions = []fv1.Function{poolmgrFunction("hello", "pkg", "default")}
		fr.Packages = []fv1.Package{{Name: "pkg", Namespace: "default"}}
		fr.FunctionAliases = []fv1.FunctionAlias{{
			Name: "prod", Namespace: "default",
			// Neither Version nor PackageDigest set: invalid per FunctionAliasSpec.Validate.
			Spec: fv1.FunctionAliasSpec{FunctionName: "hello"},
		}}
		_, err := validateWith(t, fr)
		require.ErrorContains(t, err, "exactly one of version or packageDigest must be set")
	})

	t.Run("environment with both container and pod spec warns", func(t *testing.T) {
		fr := newFissionResources()
		env := fv1.Environment{Name: "env", Namespace: "default"}
		env.Spec.Runtime.Container = &apiv1.Container{}
		env.Spec.Runtime.PodSpec = &apiv1.PodSpec{}
		fr.Environments = []fv1.Environment{env}
		warnings, _ := validateWith(t, fr)
		assert.Contains(t, warnings, "You have provided both - container spec and pod spec and while merging the pod spec will take precedence.")
	})
}

func TestTrackSourceMap(t *testing.T) {
	t.Parallel()
	fr := newFissionResources()
	loc := &Location{Path: "a.yaml", Line: 1}
	obj := &metav1.ObjectMeta{Name: "fn", Namespace: "default"}

	require.NoError(t, fr.trackSourceMap("Function", obj, loc))
	assert.Equal(t, *loc, fr.SourceMap.Locations["Function"]["default"]["fn"])

	err := fr.trackSourceMap("Function", obj, &Location{Path: "b.yaml", Line: 2})
	require.ErrorContains(t, err, "Duplicate")
}

func TestPackageAndArchiveUploadSpecInSpecs(t *testing.T) {
	t.Parallel()
	fr := newFissionResources()
	fr.ArchiveUploadSpecs = []types.ArchiveUploadSpec{{Name: "ar", RootDir: "/root", IncludeGlobs: []string{"*.js"}}}
	fr.Packages = []fv1.Package{{Name: "pkg", Namespace: "default"}}

	assert.NotNil(t, fr.ArchiveUploadSpecInSpecs(&types.ArchiveUploadSpec{Name: "ar"}, true, false))
	assert.Nil(t, fr.ArchiveUploadSpecInSpecs(&types.ArchiveUploadSpec{Name: "nope"}, true, false))
	// compareSpec: same name, different globs is not a match.
	assert.Nil(t, fr.ArchiveUploadSpecInSpecs(&types.ArchiveUploadSpec{Name: "ar", RootDir: "/root"}, true, true))
	assert.NotNil(t, fr.ArchiveUploadSpecInSpecs(&types.ArchiveUploadSpec{Name: "ar", RootDir: "/root", IncludeGlobs: []string{"*.js"}}, true, true))

	assert.NotNil(t, fr.PackageInSpecs(&fv1.Package{Name: "pkg", Namespace: "default"}, true, false))
	assert.Nil(t, fr.PackageInSpecs(&fv1.Package{Name: "pkg", Namespace: "other"}, true, false))
}

func TestExistsInSpecs(t *testing.T) {
	t.Parallel()
	meta := metav1.ObjectMeta{Name: "x", Namespace: "default"}
	fr := newFissionResources()
	fr.ArchiveUploadSpecs = []types.ArchiveUploadSpec{{Name: "x"}}
	fr.Packages = []fv1.Package{{ObjectMeta: meta}}
	fr.Functions = []fv1.Function{{ObjectMeta: meta}}
	fr.Environments = []fv1.Environment{{ObjectMeta: meta}}
	fr.HttpTriggers = []fv1.HTTPTrigger{{ObjectMeta: meta}}
	fr.KubernetesWatchTriggers = []fv1.KubernetesWatchTrigger{{ObjectMeta: meta}}
	fr.MessageQueueTriggers = []fv1.MessageQueueTrigger{{ObjectMeta: meta}}
	fr.TimeTriggers = []fv1.TimeTrigger{{ObjectMeta: meta}}
	fr.Workflows = []fv1.Workflow{{ObjectMeta: meta}}
	fr.FunctionAliases = []fv1.FunctionAlias{{ObjectMeta: meta}}

	present := []Resource{
		&fv1.Package{ObjectMeta: meta},
		&fv1.Function{ObjectMeta: meta},
		&fv1.Environment{ObjectMeta: meta},
		&fv1.HTTPTrigger{ObjectMeta: meta},
		&fv1.KubernetesWatchTrigger{ObjectMeta: meta},
		&fv1.MessageQueueTrigger{ObjectMeta: meta},
		&fv1.TimeTrigger{ObjectMeta: meta},
		&fv1.Workflow{ObjectMeta: meta},
		&fv1.FunctionAlias{ObjectMeta: meta},
	}
	for _, res := range present {
		exists, err := fr.ExistsInSpecs(res)
		require.NoError(t, err)
		assert.True(t, exists, "%T should exist", res)
	}
	assert.True(t, fr.ArchiveUploadSpecExists("x"))
	assert.False(t, fr.ArchiveUploadSpecExists("nope"))

	exists, err := fr.ExistsInSpecs(&fv1.Function{Name: "missing", Namespace: "default"})
	require.NoError(t, err)
	assert.False(t, exists)

	// Same name, other namespace is a different resource.
	exists, err = fr.ExistsInSpecs(&fv1.Function{Name: "x", Namespace: "other"})
	require.NoError(t, err)
	assert.False(t, exists)

	_, err = fr.ExistsInSpecs(&apiv1.ConfigMap{ObjectMeta: meta})
	require.Error(t, err, "a kind the scheme does not know is an error, not a silent false")
}

func TestLocationString(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "spec.yaml:7", Location{Path: "spec.yaml", Line: 7}.String())
}
