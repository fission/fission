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
		resource any
		wantKind string
		wantName string
	}{
		{"ArchiveUploadSpec", types.ArchiveUploadSpec{Name: "ar"}, "ArchiveUploadSpec", "ar"},
		{"Package", fv1.Package{ObjectMeta: metav1.ObjectMeta{Name: "pkg"}}, "Package", "pkg"},
		{"Function", fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn"}}, "Function", "fn"},
		{"Environment", fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "env"}}, "Environment", "env"},
		{"HTTPTrigger", fv1.HTTPTrigger{ObjectMeta: metav1.ObjectMeta{Name: "ht"}}, "HTTPTrigger", "ht"},
		{"KubernetesWatchTrigger", fv1.KubernetesWatchTrigger{ObjectMeta: metav1.ObjectMeta{Name: "kw"}}, "KubernetesWatchTrigger", "kw"},
		{"MessageQueueTrigger", fv1.MessageQueueTrigger{ObjectMeta: metav1.ObjectMeta{Name: "mqt"}}, "MessageQueueTrigger", "mqt"},
		{"TimeTrigger", fv1.TimeTrigger{ObjectMeta: metav1.ObjectMeta{Name: "tt"}}, "TimeTrigger", "tt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			meta, kind, data, err := crdToYaml(tt.resource)
			require.NoError(t, err)
			assert.Equal(t, tt.wantKind, kind)
			assert.Equal(t, tt.wantName, meta.Name)
			assert.NotEmpty(t, data)
		})
	}

	t.Run("unknown type errors", func(t *testing.T) {
		t.Parallel()
		_, _, _, err := crdToYaml(42)
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
	fn := fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
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
		pkg := fv1.Package{ObjectMeta: metav1.ObjectMeta{Name: "pkg", Namespace: "default"}}
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
		ht := fv1.HTTPTrigger{ObjectMeta: metav1.ObjectMeta{Name: "ht", Namespace: "default"}}
		ht.Kind = "HTTPTrigger"
		ht.Spec.FunctionReference = fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "ghost"}
		fr.HttpTriggers = []fv1.HTTPTrigger{ht}
		_, err := validateWith(t, fr)
		require.ErrorContains(t, err, "references unknown function")
	})

	t.Run("container executor skips the package reference check", func(t *testing.T) {
		fr := newFissionResources()
		fn := fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "cfn", Namespace: "default"}}
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
		pkg := fv1.Package{ObjectMeta: metav1.ObjectMeta{Name: "pkg", Namespace: "default"}}
		fr.Functions = []fv1.Function{fn}
		fr.Packages = []fv1.Package{pkg}
		warnings, _ := validateWith(t, fr)
		assert.Contains(t, warnings, "Configmap cfg is referred in the spec but not present in the cluster")
	})

	t.Run("function referencing an undeclared environment warns", func(t *testing.T) {
		fr := newFissionResources()
		fn := poolmgrFunction("fn", "pkg", "default")
		fr.Functions = []fv1.Function{fn}
		fr.Packages = []fv1.Package{{ObjectMeta: metav1.ObjectMeta{Name: "pkg", Namespace: "default"}}}
		warnings, _ := validateWith(t, fr)
		assert.Contains(t, warnings, "Environment env is referenced in function fn but not declared in specs")
	})

	t.Run("FunctionAlias references an unknown function warns, not errors", func(t *testing.T) {
		fr := newFissionResources()
		fr.FunctionAliases = []fv1.FunctionAlias{{
			ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "ghost", Version: "ghost-v1"},
		}}
		warnings, err := validateWith(t, fr)
		require.NoError(t, err, "a dangling alias->function ref is informational (eventual consistency), not a hard error")
		require.Len(t, warnings, 1)
		assert.Contains(t, warnings[0], "FunctionAlias 'prod' references unknown function 'ghost' in the spec set")
	})

	t.Run("FunctionAlias with invalid spec errors", func(t *testing.T) {
		fr := newFissionResources()
		fr.Functions = []fv1.Function{poolmgrFunction("hello", "pkg", "default")}
		fr.Packages = []fv1.Package{{ObjectMeta: metav1.ObjectMeta{Name: "pkg", Namespace: "default"}}}
		fr.FunctionAliases = []fv1.FunctionAlias{{
			ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
			// Neither Version nor PackageDigest set: invalid per FunctionAliasSpec.Validate.
			Spec: fv1.FunctionAliasSpec{FunctionName: "hello"},
		}}
		_, err := validateWith(t, fr)
		require.ErrorContains(t, err, "exactly one of version or packageDigest must be set")
	})

	t.Run("environment with both container and pod spec warns", func(t *testing.T) {
		fr := newFissionResources()
		env := fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "env", Namespace: "default"}}
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

func TestSpecExists(t *testing.T) {
	t.Parallel()
	fr := newFissionResources()
	fr.ArchiveUploadSpecs = []types.ArchiveUploadSpec{{Name: "ar", RootDir: "/root"}}
	fr.Packages = []fv1.Package{{ObjectMeta: metav1.ObjectMeta{Name: "pkg", Namespace: "default"}}}

	assert.NotNil(t, fr.SpecExists(&types.ArchiveUploadSpec{Name: "ar"}, true, false))
	assert.Nil(t, fr.SpecExists(&types.ArchiveUploadSpec{Name: "nope"}, true, false))
	assert.NotNil(t, fr.SpecExists(&fv1.Package{ObjectMeta: metav1.ObjectMeta{Name: "pkg", Namespace: "default"}}, true, false))
	assert.Nil(t, fr.SpecExists(&fv1.Function{}, true, false)) // unimplemented type
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
	fr.FunctionAliases = []fv1.FunctionAlias{{ObjectMeta: meta}}

	present := []any{
		types.ArchiveUploadSpec{Name: "x"},
		fv1.Package{ObjectMeta: meta},
		fv1.Function{ObjectMeta: meta},
		fv1.Environment{ObjectMeta: meta},
		fv1.HTTPTrigger{ObjectMeta: meta},
		fv1.KubernetesWatchTrigger{ObjectMeta: meta},
		fv1.MessageQueueTrigger{ObjectMeta: meta},
		fv1.TimeTrigger{ObjectMeta: meta},
		fv1.FunctionAlias{ObjectMeta: meta},
	}
	for _, res := range present {
		exists, err := fr.ExistsInSpecs(res)
		require.NoError(t, err)
		assert.True(t, exists, "%T should exist", res)
	}

	exists, err := fr.ExistsInSpecs(fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "missing", Namespace: "default"}})
	require.NoError(t, err)
	assert.False(t, exists)

	_, err = fr.ExistsInSpecs("not a resource")
	require.Error(t, err)
}

func TestLocationString(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "spec.yaml:7", Location{Path: "spec.yaml", Line: 7}.String())
}
