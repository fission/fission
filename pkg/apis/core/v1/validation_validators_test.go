// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/mqtrigger/validator"
)

// TestMessageQueueTriggerSpecValidateForAdmission: Topic, ResponseTopic AND
// ErrorTopic are all validated for a classic-kind trigger — an invalid
// ErrorTopic is the worst of the three to admit, since the consumer refuses to
// advance past a poison event whose error-topic publish keeps failing (E5) and
// the trigger wedges.
func TestMessageQueueTriggerSpecValidateForAdmission(t *testing.T) {
	t.Parallel()
	validator.Register("test-classic-mq", func(topic string) bool { return !strings.Contains(topic, "/") })

	base := func(mutate func(*MessageQueueTriggerSpec)) MessageQueueTriggerSpec {
		spec := MessageQueueTriggerSpec{
			FunctionReference: FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "fn"},
			MessageQueueType:  "test-classic-mq",
			MqtKind:           "fission",
			Topic:             "orders",
		}
		if mutate != nil {
			mutate(&spec)
		}
		return spec
	}

	require.NoError(t, base(nil).validateForAdmission())
	require.NoError(t, base(func(s *MessageQueueTriggerSpec) { s.ResponseTopic, s.ErrorTopic = "replies", "errs" }).validateForAdmission())
	require.Error(t, base(func(s *MessageQueueTriggerSpec) { s.Topic = "bad/topic" }).validateForAdmission())
	require.Error(t, base(func(s *MessageQueueTriggerSpec) { s.ResponseTopic = "bad/topic" }).validateForAdmission())
	require.Error(t, base(func(s *MessageQueueTriggerSpec) { s.ErrorTopic = "bad/topic" }).validateForAdmission())
	require.Error(t, base(func(s *MessageQueueTriggerSpec) { s.MessageQueueType = "unregistered" }).validateForAdmission())
}

func TestValidateKubeName(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateKubeName("f", "valid-name"))
	require.Error(t, ValidateKubeName("f", "Invalid_Name"))
	require.Error(t, ValidateKubeName("f", "-bad"))
}

func TestValidateKubePort(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateKubePort("p", 8080))
	require.Error(t, ValidateKubePort("p", -1))
	require.Error(t, ValidateKubePort("p", 70000))
}

func TestValidateKubeLabel(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateKubeLabel("l", map[string]string{}), "empty labels are valid")
	require.Error(t, ValidateKubeLabel("l", map[string]string{"in valid key": "v"}))
}

func TestValidateKubeReference(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateKubeReference("ref", "name", "ns"))
	require.NoError(t, ValidateKubeReference("ref", "name", ""), "empty namespace is allowed")
	require.Error(t, ValidateKubeReference("ref", "Bad_Name", "ns"))
	require.Error(t, ValidateKubeReference("ref", "name", "Bad_NS"))
}

func TestIsValidCronSpec(t *testing.T) {
	t.Parallel()
	require.NoError(t, IsValidCronSpec("0 * * * *"))
	require.NoError(t, IsValidCronSpec("@every 1h"))
	require.Error(t, IsValidCronSpec("not a cron spec"))
}

func TestChecksumValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, Checksum{Type: ChecksumTypeSHA256}.Validate())
	require.Error(t, Checksum{Type: "md5"}.Validate())
}

func TestArchiveValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, Archive{Type: ArchiveTypeLiteral}.Validate())
	require.NoError(t, Archive{Type: ArchiveTypeUrl}.Validate())
	require.Error(t, Archive{Type: "tarball"}.Validate())
	require.Error(t, Archive{Checksum: Checksum{Type: "md5"}}.Validate())
}

func TestArchiveValidateOCI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		archive Archive
		wantErr bool
	}{
		{"oci only is valid", Archive{Type: ArchiveTypeOCI, OCI: &OCIArchive{Image: "ghcr.io/x/y:v1"}}, false},
		{"empty archive stays valid", Archive{}, false},
		{"literal plus oci rejected", Archive{Literal: []byte("x"), OCI: &OCIArchive{Image: "ghcr.io/x/y:v1"}}, true},
		{"url plus oci rejected", Archive{URL: "http://example.com/a.zip", OCI: &OCIArchive{Image: "ghcr.io/x/y:v1"}}, true},
		{"invalid nested oci rejected", Archive{Type: ArchiveTypeOCI, OCI: &OCIArchive{}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.archive.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestOCIArchiveValidate(t *testing.T) {
	t.Parallel()
	validDigest := "sha256:" + strings.Repeat("ab", 32)
	require.NoError(t, OCIArchive{Image: "ghcr.io/x/y:v1"}.Validate())
	require.NoError(t, OCIArchive{Image: "ghcr.io/x/y:v1", Digest: validDigest}.Validate())
	require.Error(t, OCIArchive{}.Validate(), "image is required")
	require.Error(t, OCIArchive{Image: "ghcr.io/x/y:v1", Digest: "sha1:abcd"}.Validate())
	require.Error(t, OCIArchive{Image: "ghcr.io/x/y:v1", Digest: "sha256:abcd"}.Validate(), "digest hex too short")

	// A digest may live in the reference or the field, not both.
	require.NoError(t, OCIArchive{Image: "ghcr.io/x/y@" + validDigest}.Validate())
	require.Error(t, OCIArchive{Image: "ghcr.io/x/y@" + validDigest, Digest: validDigest}.Validate(),
		"digest in both the reference and the field must be rejected")

	// SubPath must be a clean relative path.
	require.NoError(t, OCIArchive{Image: "ghcr.io/x/y:v1", SubPath: "app"}.Validate())
	require.NoError(t, OCIArchive{Image: "ghcr.io/x/y:v1", SubPath: "app/code"}.Validate())
	require.NoError(t, OCIArchive{Image: "ghcr.io/x/y:v1", SubPath: ""}.Validate())
	require.Error(t, OCIArchive{Image: "ghcr.io/x/y:v1", SubPath: "/abs"}.Validate(), "absolute sub-path")
	require.Error(t, OCIArchive{Image: "ghcr.io/x/y:v1", SubPath: "../escape"}.Validate(), "traversing sub-path")
	require.Error(t, OCIArchive{Image: "ghcr.io/x/y:v1", SubPath: "a/../b"}.Validate(), "unclean sub-path")
}

func TestPackageSpecValidateRejectsOCISource(t *testing.T) {
	t.Parallel()
	spec := PackageSpec{
		Environment: EnvironmentReference{Name: "env", Namespace: "ns"},
		Source:      Archive{Type: ArchiveTypeOCI, OCI: &OCIArchive{Image: "ghcr.io/x/y:v1"}},
	}
	err := spec.Validate()
	require.Error(t, err, "oci on the source archive must be rejected")
	require.Contains(t, err.Error(), "deployment archive only")

	spec = PackageSpec{
		Environment: EnvironmentReference{Name: "env", Namespace: "ns"},
		Deployment:  Archive{Type: ArchiveTypeOCI, OCI: &OCIArchive{Image: "ghcr.io/x/y:v1"}},
	}
	require.NoError(t, spec.Validate())
}

func TestArchiveIsEmptyOCI(t *testing.T) {
	t.Parallel()
	require.True(t, Archive{}.IsEmpty())
	require.False(t, Archive{OCI: &OCIArchive{Image: "ghcr.io/x/y:v1"}}.IsEmpty())
	require.False(t, Archive{Literal: []byte("x")}.IsEmpty())
	require.False(t, Archive{URL: "http://example.com/a.zip"}.IsEmpty())
}

func TestReferenceValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, EnvironmentReference{Name: "env", Namespace: "ns"}.Validate())
	require.NoError(t, SecretReference{Name: "sec"}.Validate())
	require.NoError(t, ConfigMapReference{Name: "cm"}.Validate())
	require.NoError(t, PackageRef{Name: "pkg"}.Validate())
	require.Error(t, EnvironmentReference{Name: "Bad_Name"}.Validate())
}

func TestPackageStatusValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, PackageStatus{BuildStatus: BuildStatusSucceeded}.Validate())
	// Empty BuildStatus is the not-yet-processed state admitted on create once
	// the /status subresource strips the defaulting webhook's status.
	require.NoError(t, PackageStatus{}.Validate())
	require.Error(t, PackageStatus{BuildStatus: "weird"}.Validate())
}

func TestExecutionStrategyValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, ExecutionStrategy{ExecutorType: ExecutorTypePoolmgr}.Validate())

	require.Error(t, ExecutionStrategy{ExecutorType: "bad"}.Validate())

	t.Run("newdeploy scale bounds", func(t *testing.T) {
		require.NoError(t, ExecutionStrategy{ExecutorType: ExecutorTypeNewdeploy, MinScale: 1, MaxScale: 3, TargetCPUPercent: 50}.Validate())
		require.Error(t, ExecutionStrategy{ExecutorType: ExecutorTypeNewdeploy, MaxScale: 0}.Validate(), "max scale must be > 0")
		require.Error(t, ExecutionStrategy{ExecutorType: ExecutorTypeNewdeploy, MinScale: 5, MaxScale: 2}.Validate(), "max < min")
		require.Error(t, ExecutionStrategy{ExecutorType: ExecutorTypeNewdeploy, MaxScale: 3, TargetCPUPercent: 200}.Validate(), "cpu out of range")
	})
}

func TestInvokeStrategyValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, InvokeStrategy{
		StrategyType:      StrategyTypeExecution,
		ExecutionStrategy: ExecutionStrategy{ExecutorType: ExecutorTypePoolmgr},
	}.Validate())
	require.Error(t, InvokeStrategy{StrategyType: "weird", ExecutionStrategy: ExecutionStrategy{ExecutorType: ExecutorTypePoolmgr}}.Validate())
}

func TestFunctionReferenceValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"}.Validate())
	require.NoError(t, FunctionReference{Type: FunctionReferenceTypeFunctionWeights}.Validate())
	require.Error(t, FunctionReference{Type: "bogus"}.Validate())
	require.Error(t, FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "Bad_Name"}.Validate())

	// RFC-0025: Alias/Version are format-only checks (kube-name), mutually
	// exclusive, and valid only when Type is "name". No existence check —
	// aliases are eventually consistent and existence-at-admission would
	// break apply-before-publish ordering.
	require.NoError(t, FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello", Alias: "prod"}.Validate(),
		"alias alone is valid with type=name")
	require.NoError(t, FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello", Version: "hello-v1"}.Validate(),
		"version alone is valid with type=name")
	require.Error(t, FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello", Alias: "prod", Version: "hello-v1"}.Validate(),
		"alias and version together must be rejected (mutually exclusive)")
	require.Error(t, FunctionReference{Type: FunctionReferenceTypeFunctionWeights, Alias: "prod"}.Validate(),
		"alias with type=function-weights must be rejected")
	require.Error(t, FunctionReference{Type: FunctionReferenceTypeFunctionWeights, Version: "hello-v1"}.Validate(),
		"version with type=function-weights must be rejected")
	require.Error(t, FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello", Alias: "Bad_Alias"}.Validate(),
		"malformed alias must be rejected")
	require.Error(t, FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello", Version: "Bad_Version"}.Validate(),
		"malformed version must be rejected")
}

func TestFunctionSpecValidate(t *testing.T) {
	t.Parallel()
	require.Error(t,
		FunctionSpec{InvokeStrategy: InvokeStrategy{
			StrategyType:      StrategyTypeExecution,
			ExecutionStrategy: ExecutionStrategy{ExecutorType: ExecutorTypeContainer, MaxScale: 1},
		}}.Validate(),
		"container executor without a pod spec should fail")
}

func TestRuntimeValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, Runtime{LoadEndpointPort: 8000, FunctionEndpointPort: 8001}.Validate())
	require.Error(t, Runtime{LoadEndpointPort: 70000}.Validate())
}

func TestEnvironmentSpecValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, EnvironmentSpec{Version: 2, AllowedFunctionsPerContainer: AllowedFunctionsPerContainerSingle}.Validate())
	require.Error(t, EnvironmentSpec{Version: 0}.Validate(), "version out of range")
	require.Error(t, EnvironmentSpec{Version: 2, Poolsize: -1}.Validate())
	require.Error(t, EnvironmentSpec{Version: 2, AllowedFunctionsPerContainer: "many"}.Validate())
}

func TestHTTPTriggerSpecValidate(t *testing.T) {
	t.Parallel()
	valid := HTTPTriggerSpec{
		Methods:           []string{"GET", "POST"},
		RelativeURL:       "/api/hello",
		FunctionReference: FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"},
	}
	require.NoError(t, valid.Validate())

	bad := HTTPTriggerSpec{
		Method:            "FETCH",
		RelativeURL:       "/api/hello",
		FunctionReference: FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"},
	}
	require.Error(t, bad.Validate())

	// RFC-0025: HTTPTriggerSpec.Validate reaches FunctionReference.Validate
	// via spec.FunctionReference.Validate(), so the alias/version XOR rule
	// must surface here too.
	badRef := HTTPTriggerSpec{
		Methods:     []string{"GET"},
		RelativeURL: "/api/hello",
		FunctionReference: FunctionReference{
			Type: FunctionReferenceTypeFunctionName, Name: "hello", Alias: "prod", Version: "hello-v1",
		},
	}
	require.Error(t, badRef.Validate(), "alias/version XOR must propagate through HTTPTriggerSpec.Validate")
}

// TestHTTPTriggerSpecValidate_Path covers the URL-path safety rules added for
// GHSA-vchh-r53j-8mpw. Keep these cases aligned with the CEL rules on
// HTTPTriggerSpec in types.go so the API server's admission decision and the
// Go-side Validate() decision (used by the CLI and the router reconciler's
// status-Condition path) stay in lockstep.
func TestHTTPTriggerSpecValidate_Path(t *testing.T) {
	t.Parallel()
	str := func(s string) *string { return &s }
	fnRef := FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"}

	for _, tc := range []struct {
		name        string
		relativeURL string
		prefix      *string
		wantErr     bool
		errSub      string
	}{
		// happy paths
		{name: "valid relativeurl", relativeURL: "/api/hello"},
		{name: "valid prefix", prefix: str("/api/")},
		{name: "valid both set", relativeURL: "/api/hello", prefix: str("/api/")},
		{name: "literal dot-dot inside segment is allowed", relativeURL: "/api/..foo"},
		{name: "double-dot suffix in segment is allowed", relativeURL: "/api/foo..bar"},

		// at-least-one-set
		{name: "neither set", wantErr: true, errSub: "at least one"},
		{name: "empty relativeurl and empty prefix", prefix: str(""), wantErr: true, errSub: "at least one"},

		// leading slash
		{name: "no leading slash relativeurl", relativeURL: "hello", wantErr: true, errSub: "must start with '/'"},
		{name: "no leading slash prefix", prefix: str("hello"), wantErr: true, errSub: "must start with '/'"},

		// root-only
		{name: "root-only relativeurl", relativeURL: "/", wantErr: true, errSub: "root-only"},
		{name: "root-only prefix", prefix: str("/"), wantErr: true, errSub: "root-only"},

		// `..` traversal
		{name: "traversal relativeurl", relativeURL: "/api/../admin", wantErr: true, errSub: "'..'"},
		{name: "traversal prefix", prefix: str("/api/../admin"), wantErr: true, errSub: "'..'"},
		{name: "leading traversal", relativeURL: "/..", wantErr: true, errSub: "'..'"},
		{name: "trailing traversal", relativeURL: "/api/..", wantErr: true, errSub: "'..'"},

		// router-owned exact paths
		{name: "reserved /router-healthz", relativeURL: "/router-healthz", wantErr: true, errSub: "router-owned"},
		{name: "reserved /readyz", relativeURL: "/readyz", wantErr: true, errSub: "router-owned"},
		{name: "reserved /_version", relativeURL: "/_version", wantErr: true, errSub: "router-owned"},
		{name: "reserved /auth/login", relativeURL: "/auth/login", wantErr: true, errSub: "router-owned"},
		{name: "reserved path as prefix", prefix: str("/readyz"), wantErr: true, errSub: "router-owned"},

		// router-internal /fission-function/ prefix
		{name: "internal-prefix relativeurl", relativeURL: "/fission-function/ns/fn", wantErr: true, errSub: "/fission-function/"},
		{name: "internal-prefix as Prefix field", prefix: str("/fission-function/"), wantErr: true, errSub: "/fission-function/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := HTTPTriggerSpec{
				RelativeURL:       tc.relativeURL,
				Prefix:            tc.prefix,
				FunctionReference: fnRef,
				Methods:           []string{"GET"},
			}
			err := spec.Validate()
			if tc.wantErr {
				require.Error(t, err, "expected error containing %q", tc.errSub)
				if tc.errSub != "" {
					require.Contains(t, err.Error(), tc.errSub)
				}
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestIngressConfigValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, IngressConfig{Path: "/foo", Host: "*"}.Validate())
	require.Error(t, IngressConfig{Path: "no-leading-slash"}.Validate())
}

func TestRouteConfigValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  RouteConfig
		wantErr bool
	}{
		{
			name:   "ingress provider, absolute path, wildcard host",
			config: RouteConfig{Provider: "ingress", Path: "/foo", Hostnames: []string{"*"}},
		},
		{
			name:   "gateway provider with a parentRef",
			config: RouteConfig{Provider: "gateway", Path: "/api", Hostnames: []string{"demo.example.com"}, Gateway: &GatewayRouteConfig{ParentRefs: []GatewayParentRef{{Name: "eg"}}}},
		},
		{
			name:    "unknown provider",
			config:  RouteConfig{Provider: "nginx"},
			wantErr: true,
		},
		{
			name:    "empty provider",
			config:  RouteConfig{},
			wantErr: true,
		},
		{
			name:    "non-absolute path",
			config:  RouteConfig{Provider: "gateway", Path: "no-leading-slash", Gateway: &GatewayRouteConfig{ParentRefs: []GatewayParentRef{{Name: "eg"}}}},
			wantErr: true,
		},
		{
			name:    "invalid hostname",
			config:  RouteConfig{Provider: "ingress", Hostnames: []string{"Not_A_Host"}},
			wantErr: true,
		},
		{
			name:    "gateway parentRef without a name",
			config:  RouteConfig{Provider: "gateway", Gateway: &GatewayRouteConfig{ParentRefs: []GatewayParentRef{{Name: ""}}}},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.config.Validate()
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestKubernetesWatchTriggerSpecValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, KubernetesWatchTriggerSpec{
		Type:              "POD",
		Namespace:         "default",
		FunctionReference: FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"},
	}.Validate())
	require.Error(t, KubernetesWatchTriggerSpec{
		Type:              "NOTAKIND",
		Namespace:         "default",
		FunctionReference: FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"},
	}.Validate())
	// RFC-0025: KubernetesWatchTriggerSpec.Validate reaches
	// FunctionReference.Validate via spec.FunctionReference.Validate(), so the
	// alias/version XOR rule must surface here too.
	require.Error(t, KubernetesWatchTriggerSpec{
		Type:      "POD",
		Namespace: "default",
		FunctionReference: FunctionReference{
			Type: FunctionReferenceTypeFunctionName, Name: "hello", Alias: "prod", Version: "hello-v1",
		},
	}.Validate(), "alias/version XOR must propagate through KubernetesWatchTriggerSpec.Validate")
}

func TestTimeTriggerSpecValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, TimeTriggerSpec{
		Cron:              "0 * * * *",
		FunctionReference: FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"},
	}.Validate())
	require.Error(t, TimeTriggerSpec{
		Cron:              "every-other-tuesday",
		FunctionReference: FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"},
	}.Validate())

	// RFC-0025: TimeTriggerSpec.Alias is a top-level field, distinct from the
	// embedded FunctionReference.Alias (which lives at spec.functionref.alias).
	require.NoError(t, TimeTriggerSpec{
		Cron:              "0 * * * *",
		FunctionReference: FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"},
		Alias:             "prod",
	}.Validate(), "valid top-level alias accepted")
	require.Error(t, TimeTriggerSpec{
		Cron:              "0 * * * *",
		FunctionReference: FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"},
		Alias:             "Bad_Alias",
	}.Validate(), "malformed top-level alias rejected")
	// The embedded FunctionReference's own alias/version XOR rule must also
	// propagate through TimeTriggerSpec.Validate via spec.FunctionReference.Validate().
	require.Error(t, TimeTriggerSpec{
		Cron: "0 * * * *",
		FunctionReference: FunctionReference{
			Type: FunctionReferenceTypeFunctionName, Name: "hello", Alias: "prod", Version: "hello-v1",
		},
	}.Validate(), "alias/version XOR must propagate through TimeTriggerSpec.Validate")
}

func TestMessageQueueTriggerSpecValidate(t *testing.T) {
	t.Parallel()
	validator.Register("test-classic-mq-fnref", func(topic string) bool { return !strings.Contains(topic, "/") })

	require.NoError(t, MessageQueueTriggerSpec{
		FunctionReference: FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"},
		MessageQueueType:  "test-classic-mq-fnref",
		MqtKind:           "fission",
		Topic:             "orders",
	}.Validate())
	// RFC-0025: MessageQueueTriggerSpec.Validate reaches
	// FunctionReference.Validate via spec.FunctionReference.Validate().
	require.Error(t, MessageQueueTriggerSpec{
		FunctionReference: FunctionReference{
			Type: FunctionReferenceTypeFunctionName, Name: "hello", Alias: "prod", Version: "hello-v1",
		},
		MessageQueueType: "test-classic-mq-fnref",
		MqtKind:          "fission",
		Topic:            "orders",
	}.Validate(), "alias/version XOR must propagate through MessageQueueTriggerSpec.Validate")
}

func TestCRDValidate(t *testing.T) {
	t.Parallel()
	meta := metav1.ObjectMeta{Name: "ok", Namespace: "default"}

	t.Run("function", func(t *testing.T) {
		f := &Function{ObjectMeta: meta}
		f.Spec.InvokeStrategy = InvokeStrategy{StrategyType: StrategyTypeExecution, ExecutionStrategy: ExecutionStrategy{ExecutorType: ExecutorTypePoolmgr}}
		require.NoError(t, f.Validate())

		bad := &Function{ObjectMeta: metav1.ObjectMeta{Name: "Bad_Name"}}
		require.Error(t, bad.Validate())
	})

	t.Run("package", func(t *testing.T) {
		p := &Package{ObjectMeta: meta}
		p.Spec.Environment = EnvironmentReference{Name: "env"}
		p.Status.BuildStatus = BuildStatusSucceeded
		require.NoError(t, p.Validate())
	})

	t.Run("environment", func(t *testing.T) {
		e := &Environment{ObjectMeta: meta}
		e.Spec.Version = 2
		require.NoError(t, e.Validate())
	})

	t.Run("httptrigger", func(t *testing.T) {
		h := &HTTPTrigger{ObjectMeta: meta}
		h.Spec.FunctionReference = FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"}
		h.Spec.RelativeURL = "/api/hello"
		require.NoError(t, h.Validate())
	})
}

func TestListValidate(t *testing.T) {
	t.Parallel()
	good := metav1.ObjectMeta{Name: "ok", Namespace: "default"}
	bad := metav1.ObjectMeta{Name: "Bad_Name"}

	fl := &FunctionList{Items: []Function{{ObjectMeta: good}, {ObjectMeta: bad}}}
	require.Error(t, fl.Validate(), "list propagates an invalid item's error")

	pl := &PackageList{Items: []Package{{
		ObjectMeta: good,
		Spec:       PackageSpec{Environment: EnvironmentReference{Name: "env"}},
		Status:     PackageStatus{BuildStatus: BuildStatusSucceeded},
	}}}
	require.NoError(t, pl.Validate())
}
