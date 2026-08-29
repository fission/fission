// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	apiv1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/agent/templates"
	"github.com/fission/fission/pkg/fission-cli/cmd/function"
	_package "github.com/fission/fission/pkg/fission-cli/cmd/package"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/svcinfo"
)

// defaultLang is `--lang`'s fallback when the flag carries no value -- both
// when a real CLI invocation leaves the (DefaultString: "node") flag
// untouched, and, since flag.FlagSet.Optional isn't consulted by the
// dummy/unit-test Input, when a test never sets it at all.
const defaultLang = "node"

// defaultTemplate is `--template`'s recognized fallback value (PR-1,
// abstraction A / Q35 resolution): the same dummy/unit-test-Input caveat as
// defaultLang applies -- a real CLI invocation gets defaultTemplate from
// flag.FnAgentTemplate's DefaultString, but a test Input that never sets
// --template needs this fallback applied explicitly. The other recognized
// value, "interpreter", has no matching Go constant: every place that used
// to compare tmplKind against one now looks it up in
// templates.Descriptors (kind-specific behavior lives there, colocated with
// the kinds map templates.Render already owns -- see KindDescriptor's doc
// comment).
const defaultTemplate = "agent"

// interpreterMaxTimeoutSeconds mirrors templates.InterpreterMaxTimeoutSeconds,
// the single source of truth also rendered into
// templates/interpreter.{js,py}.tmpl's MAX_TIMEOUT_SECONDS -- see that
// constant's doc comment. This local alias just saves every call site below
// from spelling out the package-qualified name; it is also, via
// templates.Descriptors["interpreter"].FunctionTimeoutSeconds, the value
// buildFunction's descriptor-table lookup applies. create_test.go's
// TestAgentCreate_TemplateInterpreter_DirectMode still asserts the rendered
// template actually contains "MAX_TIMEOUT_SECONDS = <this constant>", which
// now checks the two sides of a single value rather than two independent
// literals. The scaffolded Function's own FunctionTimeout is set to match
// (see buildFunction) so the platform's own request timeout does not kill a
// full-length exec call substantially ahead of the template's own timeout;
// at the exact ceiling the two race (the platform's clock starts first), so
// this is a "close together", not a strict "template always wins",
// guarantee.
const interpreterMaxTimeoutSeconds = templates.InterpreterMaxTimeoutSeconds

// scaffoldFunctionTimeout mirrors `fn create`'s own --fntimeout default of
// 60 (flag.FnExecutionTimeout's DefaultInt, cmd/function's flag.go). This
// command intentionally exposes no --fntimeout/--concurrency/--executortype
// flags (a minimal, opinionated scaffold): FunctionTimeout is hardcoded here
// rather than left at Go's zero value, which would produce a Function with
// no timeout at all; Concurrency/RequestsPerPod/RetainPods are deliberately
// left at zero to match the demo shape (demo/agent-boardroom/specs/
// support-desk.yaml carries none of those keys either).
const scaffoldFunctionTimeout = 60

type CreateSubCommand struct {
	cmd.CommandActioner
}

// scaffoldInfo carries the fields do() derives across its guard/render/spec
// steps that both buildFunction and printNextSteps need in order to build
// the scaffolded Function and its next-steps output -- one struct instead
// of the two hand-kept positional-parameter lists (7 for buildFunction, 9
// for printNextSteps) whose argument order used to be the only thing
// keeping caller and callee in sync. Populated incrementally as do() learns
// each field: CodePath only after the handler write, EnvExists only after
// checkEnvironment runs.
type scaffoldInfo struct {
	Name      string
	Namespace string
	EnvName   string
	TmplKind  string
	CodePath  string
	SpecDir   string
	ToSpec    bool
	EnvExists bool
}

// Create implements `fission agent create`: it writes one of Task 1's
// embedded handler templates to disk, then composes `fn create`'s shipped
// pipeline (_package.CreatePackage, function.GetStateConfig/GetAgentConfig,
// spec.SaveOrDry) around it to produce a working Package + Function --
// State (keyed on the function name, sticky on the session header) and
// Agent -- in one command, in both direct and --spec modes. See
// docs/superpowers/plans/2026-08-26-agent-runtime-scaffold.md Task 2.
func Create(input cli.Input) error {
	return (&CreateSubCommand{}).do(input)
}

func (opts *CreateSubCommand) do(input cli.Input) error {
	name := input.String(flagkey.FnName)

	lang := input.String(flagkey.FnAgentLang)
	if lang == "" {
		lang = defaultLang
	}

	tmplKind := input.String(flagkey.FnAgentTemplate)
	if tmplKind == "" {
		tmplKind = defaultTemplate
	}

	envName := input.String(flagkey.FnEnvironmentName)
	if envName == "" {
		// The demo's own Environment/handler-language naming convention
		// (demo/agent-boardroom/specs/support-desk.yaml: environment name
		// "node" for the node handler) -- and the same string the
		// env-missing next-steps line derives its
		// ghcr.io/fission/<env>-env image name from.
		envName = lang
	}

	toSpec := input.Bool(flagkey.SpecSave)
	specDir := util.GetSpecDir(input)
	specIgnore := util.GetSpecIgnore(input)
	specFile := ""
	if toSpec {
		specFile = fmt.Sprintf("agent-%s.yaml", name)
	}

	userProvidedNS, fnNamespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error retrieving namespace information: %w", err)
	}

	info := scaffoldInfo{
		Name:      name,
		Namespace: fnNamespace,
		EnvName:   envName,
		TmplKind:  tmplKind,
		SpecDir:   specDir,
		ToSpec:    toSpec,
	}

	// Duplicate-name guard (direct mode only): refuse a duplicate function
	// name BEFORE writing anything to disk, so a refused create never
	// strands a scaffolded handler file (same duplicate check `fn create`
	// runs, cmd/function/create.go:414-422). In --spec mode there is no live
	// cluster object to collide with at this point.
	if !toSpec {
		fn, err := opts.Client().FissionClientSet.CoreV1().Functions(fnNamespace).Get(input.Context(), name, metav1.GetOptions{})
		if err != nil && !k8serrors.IsNotFound(err) {
			return err
		} else if fn.Name != "" && fn.Namespace != "" {
			return errors.New("a function with the same name already exists")
		}
	}

	// Tool-config validation guard: validate --tool-* flags -- an unreadable
	// --tool-input-schema file (function.GetToolConfig's own os.ReadFile)
	// and a readable-but-content-invalid one (validateToolInputSchema,
	// mirroring fv1.ToolConfig.Validate's own InputSchema structural check)
	// -- BEFORE any side effect below. Same "validate before touching
	// disk/cluster" discipline the duplicate-name guard above applies, and
	// what `fn create` gets for free by validating tool config in
	// complete() before create() runs. buildFunction receives the
	// already-validated result so neither failure mode strands a scaffolded
	// handler file or an uploaded-but-orphaned Package.
	toolCfg, err := function.GetToolConfig(input, nil)
	if err != nil {
		return err
	}
	if toolCfg != nil && toolCfg.InputSchema != nil && len(toolCfg.InputSchema.Raw) > 0 {
		if err := validateToolInputSchema(toolCfg.InputSchema.Raw); err != nil {
			return err
		}
	}

	// Handler render + write: render the handler and write it to disk,
	// refusing to clobber an existing file (an idempotent re-run must point
	// at a different --code, never silently overwrite someone's edited
	// handler).
	rendered, ext, err := templates.Render(tmplKind, lang, name)
	if err != nil {
		return err
	}
	codePath := input.String(flagkey.PkgCode)
	if codePath == "" {
		codePath = fmt.Sprintf("%s.%s", name, ext)
	}
	if _, statErr := os.Stat(codePath); statErr == nil {
		return fmt.Errorf("refusing to overwrite existing file %q; pass --%s to scaffold at a different path", codePath, flagkey.PkgCode)
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("checking handler file %q: %w", codePath, statErr)
	}
	if err := os.WriteFile(codePath, rendered, 0o644); err != nil { //nolint:gosec // a scaffolded handler is meant to be read by its author, not secret
		return fmt.Errorf("writing handler file %q: %w", codePath, err)
	}
	console.Info(fmt.Sprintf("Scaffolded %s handler: %s", lang, codePath))
	info.CodePath = codePath

	// Spec-dir auto-init (--spec mode only): mint a fresh spec directory
	// when none exists yet (spec.go's save() precondition, spec.go:140-142,
	// is what every write below hits otherwise: "couldn't find specs, run
	// `fission spec init` first"). A directory that already has
	// fission-deployment-config.yaml is used silently and never touched --
	// this only mints one when it is genuinely absent.
	if toSpec {
		if err := ensureSpecInitialized(input, specDir); err != nil {
			return err
		}
	}

	// Environment existence check: warn, don't create (fn-create precedent,
	// cmd/function/create.go:507-542).
	envExists, err := opts.checkEnvironment(input, name, envName, fnNamespace, userProvidedNS, toSpec, specDir, specIgnore)
	if err != nil {
		return err
	}
	info.EnvExists = envExists

	// Package + Function build: build (and, in direct mode, upload) the
	// Package from the handler file just written. noZip=true and a
	// single-file deployArchiveFiles mirrors `fn create --code`'s own path
	// (cmd/function/create.go:548-554).
	pkgMeta, err := _package.CreatePackage(input, opts.Client(), "", fnNamespace, envName,
		nil, []string{codePath}, "", specDir, specFile, true, userProvidedNS, "")
	if err != nil {
		return fmt.Errorf("error creating package: %w", err)
	}

	fn := buildFunction(input, info, pkgMeta, toolCfg)
	if toSpec {
		fn.Namespace = userProvidedNS
		fn.Spec.Package.PackageRef.Namespace = userProvidedNS
		fn.Spec.Environment.Namespace = userProvidedNS
	}

	if handled, err := spec.SaveOrDry(input, fn, specFile); handled {
		if err != nil {
			return err
		}
	} else {
		if _, err := opts.Client().FissionClientSet.CoreV1().Functions(fn.Namespace).Create(input.Context(), fn, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("error creating function: %w", err)
		}
		fmt.Fprintf(input.Stdout(), "function '%s' created\n", fn.Name)
	}

	printNextSteps(input.Stdout(), info)
	return nil
}

// validateToolInputSchema mirrors fv1.ToolConfig.Validate's InputSchema
// structural check (pkg/apis/core/v1/validation.go, reached at admission,
// only when FunctionSpec.Tool is non-nil): a supplied --tool-input-schema
// file must parse as a JSON object whose "type" is "object". Reusing that
// check's exact semantics and error wording here, before any side effect,
// closes the gap admission-only validation leaves for this command: by the
// time a Function reaches admission, do()'s caller has already written the
// handler file to disk and (direct mode) already created the Package -- see
// do()'s tool-config validation guard. See ToolConfig.Validate's own doc
// comment for why the "type": "object" check matters (the MCP SDK's AddTool
// panics on anything else). Same ADMISSION LENIENCY as the webhook check:
// duplicate JSON member names and invalid UTF-8 are allowed, matching what
// a stored/GitOps-applied schema is allowed to contain.
func validateToolInputSchema(raw []byte) error {
	var obj map[string]jsontext.Value
	if err := json.Unmarshal(raw, &obj, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)); err != nil {
		return fmt.Errorf("--%s: must be a JSON object", flagkey.FnToolInputSchema)
	}
	typRaw, ok := obj["type"]
	if !ok {
		return fmt.Errorf("--%s: must be a JSON Schema object with a \"type\" key", flagkey.FnToolInputSchema)
	}
	var typ string
	if json.Unmarshal(typRaw, &typ, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)) != nil || typ != "object" {
		return fmt.Errorf("--%s: \"type\" must be \"object\" (MCP tool arguments are always an object)", flagkey.FnToolInputSchema)
	}
	return nil
}

// ensureSpecInitialized runs the equivalent of `fission spec init` when
// specDir has no fission-deployment-config.yaml yet, printing a note so the
// auto-init isn't a silent side effect. A directory that already has one is
// left completely untouched -- spec.Init itself refuses to run against an
// existing config (spec/init.go:90-92), so this check exists to skip the
// call (and its note) entirely rather than let that refusal surface as an
// error on every normal re-run.
func ensureSpecInitialized(input cli.Input, specDir string) error {
	configPath := filepath.Join(specDir, "fission-deployment-config.yaml")
	if _, err := os.Stat(configPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking for an existing spec directory %q: %w", specDir, err)
	}
	console.Info(fmt.Sprintf("no spec directory found at %q; running the equivalent of `fission spec init`", specDir))
	if err := spec.Init(input); err != nil {
		return fmt.Errorf("auto-initializing the spec directory: %w", err)
	}
	return nil
}

// checkEnvironment reports whether envName exists, warning (never erroring,
// never creating) when it does not -- the same warn-don't-create contract
// `fn create` applies for both its spec (cmd/function/create.go:516-532) and
// direct (:533-541) modes.
func (opts *CreateSubCommand) checkEnvironment(input cli.Input, fnName, envName, fnNamespace, userProvidedNS string, toSpec bool, specDir, specIgnore string) (bool, error) {
	if toSpec {
		fr, err := spec.ReadSpecs(specDir, specIgnore, false)
		if err != nil {
			return false, fmt.Errorf("error reading spec in '%s': %w", specDir, err)
		}
		exists, err := fr.ExistsInSpecs(&fv1.Environment{
			ObjectMeta: metav1.ObjectMeta{Name: envName, Namespace: userProvidedNS},
		})
		if err != nil {
			return false, err
		}
		if !exists {
			console.Warn(fmt.Sprintf("Function '%s' references unknown Environment '%s', please create it before applying spec", fnName, envName))
		}
		return exists, nil
	}

	_, err := opts.Client().FissionClientSet.CoreV1().Environments(fnNamespace).Get(input.Context(), envName, metav1.GetOptions{})
	if err != nil {
		// fn create's own env-missing check (create.go:536) only recognizes a
		// ferror.Error with Code ErrorNotFound -- but a typed clientset (the
		// fake one these tests use, AND, confirmed against a live cluster
		// while building this command, the real one too) returns a plain
		// k8s.io/apimachinery StatusError, which that assertion never
		// matches. Left as fn create left it, "not found" would silently
		// become a hard error instead of a warning, making half of Global
		// Constraints item 6 (env-missing -> warn, never block) unreachable.
		// k8serrors.IsNotFound is checked first, then the ferror.Error form
		// is still recognized as a belt-and-suspenders fallback in case some
		// other client path really does wrap errors that way.
		if k8serrors.IsNotFound(err) {
			console.Warn(fmt.Sprintf("Environment \"%s\" does not exist. Please create the environment before invoking the agent. \nFor example: `fission env create --name %s --namespace %s --image ghcr.io/fission/%s-env`\n", envName, envName, fnNamespace, envName))
			return false, nil
		}
		if e, ok := err.(ferror.Error); ok && e.Code == ferror.ErrorNotFound {
			console.Warn(fmt.Sprintf("Environment \"%s\" does not exist. Please create the environment before invoking the agent. \nFor example: `fission env create --name %s --namespace %s --image ghcr.io/fission/%s-env`\n", envName, envName, fnNamespace, envName))
			return false, nil
		}
		return false, fmt.Errorf("error retrieving environment information: %w", err)
	}
	return true, nil
}

// buildFunction assembles the scaffolded Function: an env reference to the
// just-created Package, plus the demo shape's State (keyspace = the
// function name, sticky routing on the platform's session header) and Agent
// blocks -- built through the SHARED function.GetStateConfig/GetAgentConfig
// helpers so this command's on/off and merge semantics never drift from
// `fn create`/`fn update`'s. Keyspace/Sticky are filled in here, on top of
// the helper's result, only when the corresponding --state* flag was left
// unset: unlike the session id (whose nil default already IS the platform's
// X-Fission-Session header, fv1 types.go:1522-1524), sticky routing has no
// such built-in default -- it is opt-in, so a scaffold that wants the demo's
// "the router lands on the same pod the dispatcher predicts" property has to
// set it explicitly.
//
// MCP exposure (PR-1) reuses function.GetToolConfig -- the SAME
// --expose-as-mcp/--tool-* flag-merge helper `fn create`/`fn update` use --
// so a scaffolded interpreter can be registered as an MCP tool with no
// second implementation of that flag handling to drift out of sync.
// toolCfg is computed by the caller, BEFORE any side effect (handler-file
// write, Package create/upload), so a bad --tool-input-schema path (an
// unreadable file, or a readable but content-invalid schema) fails fast
// instead of stranding an orphaned Package or handler file -- see do()'s
// tool-config validation guard.
//
// info.TmplKind selects the scaffolded Function's own FunctionTimeout via a
// templates.Descriptors lookup: the interpreter kind's
// FunctionTimeoutSeconds (interpreterMaxTimeoutSeconds, matching the
// interpreter template's own request-timeoutSeconds ceiling) overrides
// scaffoldFunctionTimeout (60, `fn create`'s own default) so the platform's
// request timeout is never what kills a full-length exec call; every other
// kind's zero-value descriptor leaves the default in place.
func buildFunction(input cli.Input, info scaffoldInfo, pkgMeta *metav1.ObjectMeta, toolCfg *fv1.ToolConfig) *fv1.Function {
	stateCfg := function.GetStateConfig(input, nil)
	if stateCfg != nil {
		if stateCfg.Keyspace == "" {
			stateCfg.Keyspace = info.Name
		}
		if stateCfg.Sticky == nil {
			stateCfg.Sticky = &fv1.StickyConfig{
				Source: fv1.StickySourceHeader,
				Name:   fv1.DefaultAgentSessionHeader,
			}
		}
	}
	agentCfg := function.GetAgentConfig(input, nil)

	fnTimeout := scaffoldFunctionTimeout
	if desc := templates.Descriptors[info.TmplKind]; desc.FunctionTimeoutSeconds > 0 {
		fnTimeout = desc.FunctionTimeoutSeconds
	}

	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: info.Name, Namespace: info.Namespace},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{Name: info.EnvName, Namespace: info.Namespace},
			Package: fv1.FunctionPackageRef{
				PackageRef: fv1.PackageRef{
					Namespace:       pkgMeta.Namespace,
					Name:            pkgMeta.Name,
					ResourceVersion: pkgMeta.ResourceVersion,
				},
			},
			InvokeStrategy: fv1.InvokeStrategy{
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypePoolmgr,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
				StrategyType: fv1.StrategyTypeExecution,
			},
			FunctionTimeout: fnTimeout,
			Resources:       apiv1.ResourceRequirements{},
			State:           stateCfg,
			Agent:           agentCfg,
			Tool:            toolCfg,
		},
	}
}

// printNextSteps writes the scaffold's <10-minute-to-first-turn punch list:
// the file it wrote, the interpreter template's trust-boundary note (gated
// on templates.Descriptors[info.TmplKind].RecommendGvisor, PR-1), how to get
// the resource(s) onto a cluster (conditional on info.ToSpec), the
// env-create line when checkEnvironment found none (conditional, and always
// step 1 when present -- Global Constraints item 6), the dispatch curl (POST
// /agents/<ns>/<name> + the platform's session header, pinned via
// fv1.DefaultAgentSessionHeader/svcinfo rather than retyped literals -- the
// same contract-drift discipline Task 1's tests apply; the exec verb's own
// wire-contract body via the descriptor's ExampleTurnBody for kinds that
// need one), and where to look afterwards. This is a spec'd, golden-tested
// deliverable (create_test.go) -- keep any wording change here in sync with
// the golden strings there.
//
// The trust-boundary note and step 1's env-create command share one
// --runtime-class gvisor recommendation rather than each printing their own:
// when the Environment is missing, step 1 below is what actually creates it,
// so folding the flag into that one command (and having the trust note point
// at it) avoids a second `env create` that would 409 AlreadyExists if
// someone followed both literally.
func printNextSteps(w io.Writer, info scaffoldInfo) {
	fmt.Fprintf(w, "\nScaffolded %s\n\n", info.CodePath)

	desc := templates.Descriptors[info.TmplKind]

	// Trust statement: env-scrubbing keeps ACCIDENTAL
	// credential leakage out of exec'd code, but it is not a sandbox -- a
	// process in the same container can still read the pod's own token
	// files, so exec'd code runs at the agent's own trust level. Printed
	// here (and repeated in the template's own header comment) rather than
	// only in the PR description, so it survives however someone gets to
	// this scaffold.
	if desc.RecommendGvisor {
		if info.EnvExists {
			// The Environment already exists, so no step below creates one --
			// gvisor has to be applied separately with `env update`, which
			// needs no --image guess at all (a real concern for a
			// user-supplied --env whose image this scaffold cannot know; it
			// only happens to be correct for the default node/python env
			// names).
			fmt.Fprintf(w,
				"exec contract: this handler runs the turn body's \"code\" or \"command\" at\n"+
					"the agent's OWN trust level (env-scrubbed, not sandboxed) -- fine for\n"+
					"run-your-own-code tools, not for adversarial or multi-tenant code. For\n"+
					"one more layer of isolation, pair it with:\n"+
					"    fission env update --name %s --runtime-class gvisor\n\n",
				info.EnvName)
		} else {
			// checkEnvironment found no Environment yet, so step 1 below
			// already creates it -- point at that single command instead of
			// printing a second, conflicting `env create` here.
			fmt.Fprintf(w,
				"exec contract: this handler runs the turn body's \"code\" or \"command\" at\n"+
					"the agent's OWN trust level (env-scrubbed, not sandboxed) -- fine for\n"+
					"run-your-own-code tools, not for adversarial or multi-tenant code. For\n"+
					"one more layer of isolation, step 1 below already creates the\n"+
					"environment with --runtime-class gvisor.\n\n")
		}
	}

	fmt.Fprintf(w, "Next steps:\n")

	step := 1
	if !info.EnvExists {
		envCreateCmd := fmt.Sprintf("fission env create --name %s --image ghcr.io/fission/%s-env", info.EnvName, info.EnvName)
		if desc.RecommendGvisor {
			envCreateCmd += " --runtime-class gvisor"
		}
		fmt.Fprintf(w, "  %d. Create the environment (not found in namespace %q):\n       %s\n",
			step, info.Namespace, envCreateCmd)
		step++
	}

	if info.ToSpec {
		fmt.Fprintf(w, "  %d. Apply the spec:\n       fission spec apply --specdir %s\n", step, info.SpecDir)
	} else {
		fmt.Fprintf(w, "  %d. Function %s/%s is already created.\n", step, info.Namespace, info.Name)
	}
	step++

	// A kind whose descriptor carries no ExampleTurnBody keeps the empty
	// '{}' body (the agent template's shape); the interpreter template
	// ignores an empty body's absent code/command (it 400s), so its
	// descriptor supplies a working exec payload instead.
	turnBody := "{}"
	if desc.ExampleTurnBody != "" {
		turnBody = desc.ExampleTurnBody
	}
	fmt.Fprintf(w,
		"  %d. Port-forward the agent runtime and dispatch a turn:\n"+
			"       kubectl -n %s port-forward svc/%s %d:%d\n"+
			"       curl -i -X POST http://127.0.0.1:%d/agents/%s/%s \\\n"+
			"         -H 'Content-Type: application/json' \\\n"+
			"         -H '%s: %s-demo-1' \\\n"+
			"         -d '%s'\n",
		step, fissionNamespace(), svcinfo.SvcAgentRuntime, svcinfo.PortAgentRuntime, svcinfo.PortAgentRuntime,
		svcinfo.PortAgentRuntime, info.Namespace, info.Name, fv1.DefaultAgentSessionHeader, info.Name, turnBody)
	step++

	fmt.Fprintf(w, "  %d. Inspect sessions:\n       fission agent sessions list --name %s --tree\n", step, info.Name)
}
