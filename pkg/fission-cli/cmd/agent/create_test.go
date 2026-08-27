// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	spectypes "github.com/fission/fission/pkg/fission-cli/cmd/spec/types"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/svcinfo"
)

// setAgentCreateClient installs a fresh fake Fission clientset as the
// package-level default client (the same sync.Once-guarded seam
// list_test.go's TestAgentListOutput uses) and returns it so a test can
// query what Create actually wrote. NewSimpleClientset, not NewClientset:
// the field-managed tracker NewClientset uses for server-side-apply support
// doesn't have CRD type conversion (https://github.com/kubernetes/kubernetes/issues/126850,
// cited in the fake package's own doc comment) and fails every Create call
// here with "no type found matching ...Package" -- NewSimpleClientset's
// plain object tracker (as its doc comment says: "processes creates,
// updates and deletions as-is, without ... field management") has no such
// requirement.
func setAgentCreateClient(t *testing.T, ns string, objs ...runtime.Object) *fissionfake.Clientset {
	t.Helper()
	cs := fissionfake.NewSimpleClientset(objs...)
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: cs, Namespace: ns})
	t.Cleanup(cmd.ResetClientsetForTest)
	return cs
}

// baseCreateInput builds a dummy.Cli carrying the flags a real CLI
// invocation would already have via flag.FlagSet's DefaultBool/DefaultString
// (dummy.Cli has no notion of registered-flag defaults, see
// cliwrapper/driver/dummy/dummy.go's doc comment and
// cmd/function/agentconfig_test.go's identical pattern), so tests set them
// explicitly to mirror agentCreateStateFlag/agentCreateAgentFlag's
// DefaultBool: true (command.go) and flag.FnAgentLang's DefaultString: "node"
// (flag.go).
func baseCreateInput(name string) dummy.Cli {
	in := dummy.TestFlagSet()
	in.SetString(flagkey.FnName, name)
	in.SetBool(flagkey.FnState, true)
	in.SetBool(flagkey.FnAgent, true)
	return in
}

// readYAMLDocs splits a spec file into its "---"-separated documents, in
// the order save() (spec.go) wrote them: ArchiveUploadSpec, then Package,
// then Function (CreatePackage/CreateArchive write the first two;
// spec.SaveOrDry writes the Function last).
func readYAMLDocs(t *testing.T, path string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var docs [][]byte
	for _, part := range strings.Split(string(data), "\n---\n") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		docs = append(docs, []byte(part))
	}
	return docs
}

func TestAgentCreate_SpecMode_AutoInit(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setAgentCreateClient(t, "default")

	in := baseCreateInput("myagent")
	in.SetBool(flagkey.SpecSave, true)

	out := captureStdout(t, func() error { return Create(in) })
	assert.Contains(t, out, "running the equivalent of `fission spec init`")

	assert.FileExists(t, filepath.Join(dir, "specs", "fission-deployment-config.yaml"))
	assert.FileExists(t, filepath.Join(dir, "myagent.js"))

	docs := readYAMLDocs(t, filepath.Join(dir, "specs", "agent-myagent.yaml"))
	require.Len(t, docs, 3, "want ArchiveUploadSpec, Package, Function")

	var aus spectypes.ArchiveUploadSpec
	require.NoError(t, yaml.Unmarshal(docs[0], &aus))
	assert.Equal(t, "ArchiveUploadSpec", aus.Kind)

	var pkg fv1.Package
	require.NoError(t, yaml.Unmarshal(docs[1], &pkg))
	assert.Equal(t, "Package", pkg.Kind)
	assert.Equal(t, "node", pkg.Spec.Environment.Name)

	var fn fv1.Function
	require.NoError(t, yaml.Unmarshal(docs[2], &fn))
	assert.Equal(t, "Function", fn.Kind)
	assert.Equal(t, "myagent", fn.Name)
	require.NotNil(t, fn.Spec.Agent, "the --state/--agent DefaultBool: true shape must carry an Agent block")
	require.NotNil(t, fn.Spec.State)
	assert.Equal(t, "myagent", fn.Spec.State.Keyspace, "keyspace defaults to the function name")
	require.NotNil(t, fn.Spec.State.Sticky, "sticky routing on the session header is the demo shape")
	assert.Equal(t, fv1.StickySourceHeader, fn.Spec.State.Sticky.Source)
	assert.Equal(t, fv1.DefaultAgentSessionHeader, fn.Spec.State.Sticky.Name)
}

func TestAgentCreate_SpecMode_ExistingConfigUntouched(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setAgentCreateClient(t, "default")

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "specs"), 0o700))
	configPath := filepath.Join(dir, "specs", "fission-deployment-config.yaml")
	original := "apiVersion: fission.io/v1\nkind: DeploymentConfig\nname: foreign-app\nuid: foreign-uid\n"
	require.NoError(t, os.WriteFile(configPath, []byte(original), 0o600))

	in := baseCreateInput("myagent")
	in.SetBool(flagkey.SpecSave, true)

	out := captureStdout(t, func() error { return Create(in) })
	assert.NotContains(t, out, "fission spec init", "an existing config must never be touched or re-initialized")

	got, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, original, string(got), "the foreign config's bytes are untouched")

	assert.FileExists(t, filepath.Join(dir, "specs", "agent-myagent.yaml"), "the new spec is appended to the same spec set")
}

func TestAgentCreate_RefusesExistingHandlerFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setAgentCreateClient(t, "default")

	handlerPath := filepath.Join(dir, "myagent.js")
	const handwritten = "// hand-written, do not clobber\n"
	require.NoError(t, os.WriteFile(handlerPath, []byte(handwritten), 0o644))

	in := baseCreateInput("myagent")
	err := Create(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "myagent.js")
	assert.Contains(t, err.Error(), "refusing to overwrite")

	got, err := os.ReadFile(handlerPath)
	require.NoError(t, err)
	assert.Equal(t, handwritten, string(got), "the existing file must be left untouched")

	assert.NoDirExists(t, filepath.Join(dir, "specs"), "the refusal happens before any spec/package/function side effect")
}

// TestAgentCreate_DirectMode_DuplicateFunctionLeavesNoOrphanFile pins the W6 fix:
// the direct-mode duplicate-name check runs BEFORE the handler file is written,
// so a refused create does not strand a scaffolded handler on disk.
func TestAgentCreate_DirectMode_DuplicateFunctionLeavesNoOrphanFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	const ns = "default"
	env := &fv1.Environment{Name: "node", Namespace: ns}
	existing := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "myagent", Namespace: ns}}
	setAgentCreateClient(t, ns, env, existing)

	in := baseCreateInput("myagent")
	err := Create(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a function with the same name already exists")

	assert.NoFileExists(t, filepath.Join(dir, "myagent.js"), "the duplicate check must run before the handler write, leaving no orphan file")
}

func TestAgentCreate_DirectMode(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	const ns = "default"
	env := &fv1.Environment{Name: "node", Namespace: ns}
	cs := setAgentCreateClient(t, ns, env)

	in := baseCreateInput("myagent")
	out := captureStdout(t, func() error { return Create(in) })
	assert.Contains(t, out, "function 'myagent' created")

	fn, err := cs.CoreV1().Functions(ns).Get(context.Background(), "myagent", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, fn.Spec.Agent)
	require.NotNil(t, fn.Spec.State)
	assert.Equal(t, "node", fn.Spec.Environment.Name)
	assert.Equal(t, "myagent", fn.Spec.State.Keyspace)

	pkg, err := cs.CoreV1().Packages(ns).Get(context.Background(), fn.Spec.Package.PackageRef.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "node", pkg.Spec.Environment.Name)

	assert.FileExists(t, filepath.Join(dir, "myagent.js"))
}

func TestAgentCreate_LangPython(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setAgentCreateClient(t, "default")

	in := baseCreateInput("pyagent")
	in.SetString(flagkey.FnAgentLang, "python")
	in.SetBool(flagkey.SpecSave, true)

	_ = captureStdout(t, func() error { return Create(in) })

	assert.FileExists(t, filepath.Join(dir, "pyagent.py"))

	docs := readYAMLDocs(t, filepath.Join(dir, "specs", "agent-pyagent.yaml"))
	require.Len(t, docs, 3)
	var pkg fv1.Package
	require.NoError(t, yaml.Unmarshal(docs[1], &pkg))
	assert.Equal(t, "python", pkg.Spec.Environment.Name, "--env unset defaults to --lang")
}

// TestAgentCreate_EnvMissing_NextStepsGolden exercises the --spec path (a
// pure local ReadSpecs/ExistsInSpecs check, see checkEnvironment): the
// referenced Environment is not declared anywhere in specs/, so the
// next-steps block's step 1 must be the exact env-create line (Global
// Constraints item 6), and step 2 must be the "apply the spec" line (toSpec
// case).
func TestAgentCreate_EnvMissing_NextStepsGolden(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("FISSION_NAMESPACE", "")
	setAgentCreateClient(t, "default")

	in := baseCreateInput("myagent")
	in.SetBool(flagkey.SpecSave, true)

	out := captureStdout(t, func() error { return Create(in) })

	want := fmt.Sprintf(
		"\nScaffolded myagent.js\n\nNext steps:\n"+
			"  1. Create the environment (not found in namespace \"default\"):\n"+
			"       fission env create --name node --image ghcr.io/fission/node-env\n"+
			"  2. Apply the spec:\n"+
			"       fission spec apply --specdir specs\n"+
			"  3. Port-forward the agent runtime and dispatch a turn:\n"+
			"       kubectl -n fission port-forward svc/%s %d:%d\n"+
			"       curl -i -X POST http://127.0.0.1:%d/agents/default/myagent \\\n"+
			"         -H 'Content-Type: application/json' \\\n"+
			"         -H '%s: myagent-demo-1' \\\n"+
			"         -d '{}'\n"+
			"  4. Inspect sessions:\n"+
			"       fission agent sessions list --name myagent --tree\n",
		svcinfo.SvcAgentRuntime, svcinfo.PortAgentRuntime, svcinfo.PortAgentRuntime,
		svcinfo.PortAgentRuntime, fv1.DefaultAgentSessionHeader,
	)
	assert.Contains(t, out, want)
}

// TestAgentCreate_EnvPresent_NextStepsGolden exercises direct mode with the
// Environment already registered: no env-create step, and step 1 reports the
// function as already created (not "apply the spec").
func TestAgentCreate_EnvPresent_NextStepsGolden(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("FISSION_NAMESPACE", "")
	const ns = "default"
	env := &fv1.Environment{Name: "node", Namespace: ns}
	setAgentCreateClient(t, ns, env)

	in := baseCreateInput("myagent")

	out := captureStdout(t, func() error { return Create(in) })

	want := fmt.Sprintf(
		"\nScaffolded myagent.js\n\nNext steps:\n"+
			"  1. Function default/myagent is already created.\n"+
			"  2. Port-forward the agent runtime and dispatch a turn:\n"+
			"       kubectl -n fission port-forward svc/%s %d:%d\n"+
			"       curl -i -X POST http://127.0.0.1:%d/agents/default/myagent \\\n"+
			"         -H 'Content-Type: application/json' \\\n"+
			"         -H '%s: myagent-demo-1' \\\n"+
			"         -d '{}'\n"+
			"  3. Inspect sessions:\n"+
			"       fission agent sessions list --name myagent --tree\n",
		svcinfo.SvcAgentRuntime, svcinfo.PortAgentRuntime, svcinfo.PortAgentRuntime,
		svcinfo.PortAgentRuntime, fv1.DefaultAgentSessionHeader,
	)
	assert.Contains(t, out, want)
}

// TestAgentCreate_DirectMode_EnvMissing_WarnsAndCreates is the regression
// test for checkEnvironment's k8serrors.IsNotFound fix: a typed clientset
// Get on a missing Environment returns a plain k8s.io/apimachinery
// StatusError (confirmed against both the fake clientset here and, while
// building this command, a real cluster), which the ferror.Error-typed
// check fn create uses (create.go:536) never matches -- left unfixed, this
// would silently turn "environment not found" into a hard error instead of
// the warn-don't-create the Global Constraints require, in direct mode
// only (the --spec path's checkEnvironment branch uses ExistsInSpecs, no
// clientset error involved, so it never had this gap).
func TestAgentCreate_DirectMode_EnvMissing_WarnsAndCreates(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("FISSION_NAMESPACE", "")
	const ns = "default"
	cs := setAgentCreateClient(t, ns) // no Environment object: env missing

	in := baseCreateInput("myagent")
	out := captureStdout(t, func() error { return Create(in) })

	assert.Contains(t, out, "Environment \"node\" does not exist")
	assert.Contains(t, out, "function 'myagent' created", "env-missing warns, it does not block")

	fn, err := cs.CoreV1().Functions(ns).Get(context.Background(), "myagent", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, fn.Spec.Agent)

	want := fmt.Sprintf(
		"\nScaffolded myagent.js\n\nNext steps:\n"+
			"  1. Create the environment (not found in namespace \"default\"):\n"+
			"       fission env create --name node --image ghcr.io/fission/node-env\n"+
			"  2. Function default/myagent is already created.\n"+
			"  3. Port-forward the agent runtime and dispatch a turn:\n"+
			"       kubectl -n fission port-forward svc/%s %d:%d\n"+
			"       curl -i -X POST http://127.0.0.1:%d/agents/default/myagent \\\n"+
			"         -H 'Content-Type: application/json' \\\n"+
			"         -H '%s: myagent-demo-1' \\\n"+
			"         -d '{}'\n"+
			"  4. Inspect sessions:\n"+
			"       fission agent sessions list --name myagent --tree\n",
		svcinfo.SvcAgentRuntime, svcinfo.PortAgentRuntime, svcinfo.PortAgentRuntime,
		svcinfo.PortAgentRuntime, fv1.DefaultAgentSessionHeader,
	)
	assert.Contains(t, out, want)
}
