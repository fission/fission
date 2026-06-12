// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	"github.com/fission/fission/pkg/fetcher"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// TestLoadPackageRegistryConfig pins the env contract: off by default,
// hard-fail on a half-configured producer, soft-fail booleans.
func TestLoadPackageRegistryConfig(t *testing.T) {
	logger := loggerfactory.GetLogger()
	setAll := func(t *testing.T, vals map[string]string) {
		t.Helper()
		for _, k := range []string{"PACKAGE_REGISTRY_ENABLED", "PACKAGE_REGISTRY_REPOSITORY_PREFIX",
			"PACKAGE_REGISTRY_PUBLISHED_PREFIX", "PACKAGE_REGISTRY_PUSH_SECRET", "PACKAGE_REGISTRY_PULL_SECRET",
			"PACKAGE_REGISTRY_INSECURE_HOSTS", "PACKAGE_REGISTRY_FALLBACK_TO_STORAGE"} {
			t.Setenv(k, vals[k])
		}
	}

	t.Run("unset means off", func(t *testing.T) {
		setAll(t, nil)
		cfg, err := loadPackageRegistryConfig(logger)
		require.NoError(t, err)
		assert.False(t, cfg.enabled)
		assert.True(t, cfg.fallbackToStorage, "fallback defaults true even when off")
	})

	t.Run("enabled without a prefix hard-fails", func(t *testing.T) {
		setAll(t, map[string]string{"PACKAGE_REGISTRY_ENABLED": "true"})
		_, err := loadPackageRegistryConfig(logger)
		require.Error(t, err, "a half-configured producer must fail at startup, not at first build")
	})

	t.Run("full configuration parses", func(t *testing.T) {
		setAll(t, map[string]string{
			"PACKAGE_REGISTRY_ENABLED":             "true",
			"PACKAGE_REGISTRY_REPOSITORY_PREFIX":   "reg.example.com/org/pkgs/",
			"PACKAGE_REGISTRY_PUBLISHED_PREFIX":    "localhost:30500/pkgs/",
			"PACKAGE_REGISTRY_PUSH_SECRET":         "push-cred",
			"PACKAGE_REGISTRY_PULL_SECRET":         "pull-cred",
			"PACKAGE_REGISTRY_INSECURE_HOSTS":      "reg.example.com, localhost:30500",
			"PACKAGE_REGISTRY_FALLBACK_TO_STORAGE": "false",
		})
		cfg, err := loadPackageRegistryConfig(logger)
		require.NoError(t, err)
		assert.True(t, cfg.enabled)
		assert.Equal(t, "reg.example.com/org/pkgs", cfg.repositoryPrefix, "trailing slash trimmed")
		assert.Equal(t, "localhost:30500/pkgs", cfg.publishedPrefix)
		assert.Equal(t, []string{"reg.example.com", "localhost:30500"}, cfg.insecureHosts)
		assert.False(t, cfg.fallbackToStorage, "strict mode must survive parsing")
	})

	t.Run("garbage FALLBACK soft-fails to the safe default", func(t *testing.T) {
		setAll(t, map[string]string{
			"PACKAGE_REGISTRY_ENABLED":             "true",
			"PACKAGE_REGISTRY_REPOSITORY_PREFIX":   "reg.example.com/p",
			"PACKAGE_REGISTRY_FALLBACK_TO_STORAGE": "not-a-bool",
		})
		cfg, err := loadPackageRegistryConfig(logger)
		require.NoError(t, err)
		assert.True(t, cfg.fallbackToStorage)
	})

	t.Run("garbage ENABLED hard-fails", func(t *testing.T) {
		setAll(t, map[string]string{"PACKAGE_REGISTRY_ENABLED": "ture"})
		_, err := loadPackageRegistryConfig(logger)
		require.Error(t, err,
			"a typo'd enable flag must not silently ship tarballs forever")
	})
}

// TestOCIPushSpecFor pins the producer-side exclusions and the repository
// layout.
func TestOCIPushSpecFor(t *testing.T) {
	cfg := &packageRegistryConfig{
		enabled:           true,
		repositoryPrefix:  "reg.example.com/pkgs",
		publishedPrefix:   "localhost:30500/pkgs",
		pushSecret:        "push-cred",
		fallbackToStorage: true,
	}
	pkg := &fv1.Package{ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "team-a"}}
	env := &fv1.Environment{Spec: fv1.EnvironmentSpec{Version: 2}}

	t.Run("eligible package gets the namespaced repository", func(t *testing.T) {
		spec := cfg.ociPushSpecFor(pkg, env)
		require.NotNil(t, spec)
		assert.Equal(t, "reg.example.com/pkgs/team-a/hello", spec.Repository)
		assert.Equal(t, "localhost:30500/pkgs/team-a/hello", spec.PublishedRepository)
		assert.Equal(t, "push-cred", spec.PushSecretName)
		assert.True(t, spec.FallbackToStorage)
	})

	t.Run("disabled producer", func(t *testing.T) {
		assert.Nil(t, (&packageRegistryConfig{}).ociPushSpecFor(pkg, env))
		assert.Nil(t, (*packageRegistryConfig)(nil).ociPushSpecFor(pkg, env))
	})

	t.Run("KeepArchive env stays on tarballs", func(t *testing.T) {
		keepEnv := env.DeepCopy()
		keepEnv.Spec.KeepArchive = true
		assert.Nil(t, cfg.ociPushSpecFor(pkg, keepEnv),
			"JVM-style envs expect an archive FILE; OCI artifacts are directories")
	})

	t.Run("tarball annotation opts a package out", func(t *testing.T) {
		annotated := pkg.DeepCopy()
		annotated.Annotations = map[string]string{packageDeliveryAnnotation: packageDeliveryTarball}
		assert.Nil(t, cfg.ociPushSpecFor(annotated, env))
	})
}

// TestSetPackageOCIPublishCondition pins the publish-outcome condition
// transitions, including clearing a stale condition on a non-producer build.
func TestSetPackageOCIPublishCondition(t *testing.T) {
	status := &fv1.PackageStatus{}

	setPackageOCIPublishCondition(status, &fetcher.ArchiveUploadResponse{
		OCI: &fv1.OCIArchive{Image: "r/p:abc", Digest: "sha256:abc"},
	}, 1)
	cond := conditions.Find(status.Conditions, fv1.PackageConditionOCIPublished)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, fv1.PackageReasonOCIPublished, cond.Reason)

	setPackageOCIPublishCondition(status, &fetcher.ArchiveUploadResponse{
		ArchiveDownloadUrl: "http://storage/x",
		OCIPushError:       "registry down",
	}, 2)
	cond = conditions.Find(status.Conditions, fv1.PackageConditionOCIPublished)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, fv1.PackageReasonOCIPublishDegraded, cond.Reason)
	assert.Contains(t, cond.Message, "registry down")

	// A later non-producer build clears the stale publish condition.
	setPackageOCIPublishCondition(status, &fetcher.ArchiveUploadResponse{
		ArchiveDownloadUrl: "http://storage/y",
	}, 3)
	assert.Nil(t, conditions.Find(status.Conditions, fv1.PackageConditionOCIPublished),
		"a tarball-only build must not carry a stale publish condition")

	// nil response (status-only updates) never touches the condition.
	setPackageOCIPublishCondition(status, nil, 4)
	assert.Nil(t, conditions.Find(status.Conditions, fv1.PackageConditionOCIPublished))
}
