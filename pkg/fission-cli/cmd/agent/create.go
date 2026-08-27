// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agent

import (
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

	// 1. Direct mode only: refuse a duplicate function name BEFORE writing
	// anything to disk, so a refused create never strands a scaffolded handler
	// file (same duplicate check `fn create` runs,
	// cmd/function/create.go:414-422). In --spec mode there is no live cluster
	// object to collide with at this point.
	if !toSpec {
		fn, err := opts.Client().FissionClientSet.CoreV1().Functions(fnNamespace).Get(input.Context(), name, metav1.GetOptions{})
		if err != nil && !k8serrors.IsNotFound(err) {
			return err
		} else if fn.Name != "" && fn.Namespace != "" {
			return errors.New("a function with the same name already exists")
		}
	}

	// 2. Render the handler and write it to disk, refusing to clobber an
	// existing file (an idempotent re-run must point at a different --code,
	// never silently overwrite someone's edited handler).
	rendered, ext, err := templates.Render(lang, name)
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

	// 3. --spec mode: auto-init a fresh spec directory when none exists yet
	// (spec.go's save() precondition, spec.go:140-142, is what every write
	// below hits otherwise: "couldn't find specs, run `fission spec init`
	// first"). A directory that already has fission-deployment-config.yaml
	// is used silently and never touched -- this only mints one when it is
	// genuinely absent.
	if toSpec {
		if err := ensureSpecInitialized(input, specDir); err != nil {
			return err
		}
	}

	// 4. Environment existence: warn, don't create (fn-create precedent,
	// cmd/function/create.go:507-542).
	envExists, err := opts.checkEnvironment(input, name, envName, fnNamespace, userProvidedNS, toSpec, specDir, specIgnore)
	if err != nil {
		return err
	}

	// 5. Compose the shipped pipeline: build (and, in direct mode, upload)
	// the Package from the handler file just written. noZip=true and a
	// single-file deployArchiveFiles mirrors `fn create --code`'s own path
	// (cmd/function/create.go:548-554).
	pkgMeta, err := _package.CreatePackage(input, opts.Client(), "", fnNamespace, envName,
		nil, []string{codePath}, "", specDir, specFile, true, userProvidedNS, "")
	if err != nil {
		return fmt.Errorf("error creating package: %w", err)
	}

	fn := buildFunction(input, name, fnNamespace, envName, pkgMeta)
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

	printNextSteps(input.Stdout(), name, fnNamespace, envName, codePath, specDir, toSpec, envExists)
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
func buildFunction(input cli.Input, name, fnNamespace, envName string, pkgMeta *metav1.ObjectMeta) *fv1.Function {
	stateCfg := function.GetStateConfig(input, nil)
	if stateCfg != nil {
		if stateCfg.Keyspace == "" {
			stateCfg.Keyspace = name
		}
		if stateCfg.Sticky == nil {
			stateCfg.Sticky = &fv1.StickyConfig{
				Source: fv1.StickySourceHeader,
				Name:   fv1.DefaultAgentSessionHeader,
			}
		}
	}
	agentCfg := function.GetAgentConfig(input, nil)

	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: fnNamespace},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{Name: envName, Namespace: fnNamespace},
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
			FunctionTimeout: scaffoldFunctionTimeout,
			Resources:       apiv1.ResourceRequirements{},
			State:           stateCfg,
			Agent:           agentCfg,
		},
	}
}

// printNextSteps writes the scaffold's <10-minute-to-first-turn punch list:
// the file it wrote, how to get the resource(s) onto a cluster (conditional
// on --spec), the env-create line when checkEnvironment found none
// (conditional, and always step 1 when present -- Global Constraints item
// 6), the dispatch curl (POST /agents/<ns>/<name> + the platform's session
// header, pinned via fv1.DefaultAgentSessionHeader/svcinfo rather than
// retyped literals -- the same contract-drift discipline Task 1's tests
// apply), and where to look afterwards. This is a spec'd, golden-tested
// deliverable (create_test.go) -- keep any wording change here in sync with
// the golden strings there.
func printNextSteps(w io.Writer, name, namespace, envName, codePath, specDir string, toSpec, envExists bool) {
	fmt.Fprintf(w, "\nScaffolded %s\n\nNext steps:\n", codePath)

	step := 1
	if !envExists {
		fmt.Fprintf(w, "  %d. Create the environment (not found in namespace %q):\n       fission env create --name %s --image ghcr.io/fission/%s-env\n",
			step, namespace, envName, envName)
		step++
	}

	if toSpec {
		fmt.Fprintf(w, "  %d. Apply the spec:\n       fission spec apply --specdir %s\n", step, specDir)
	} else {
		fmt.Fprintf(w, "  %d. Function %s/%s is already created.\n", step, namespace, name)
	}
	step++

	fmt.Fprintf(w,
		"  %d. Port-forward the agent runtime and dispatch a turn:\n"+
			"       kubectl -n %s port-forward svc/%s %d:%d\n"+
			"       curl -i -X POST http://127.0.0.1:%d/agents/%s/%s \\\n"+
			"         -H 'Content-Type: application/json' \\\n"+
			"         -H '%s: %s-demo-1' \\\n"+
			"         -d '{}'\n",
		step, fissionNamespace(), svcinfo.SvcAgentRuntime, svcinfo.PortAgentRuntime, svcinfo.PortAgentRuntime,
		svcinfo.PortAgentRuntime, namespace, name, fv1.DefaultAgentSessionHeader, name)
	step++

	fmt.Fprintf(w, "  %d. Inspect sessions:\n       fission agent sessions list --name %s --tree\n", step, name)
}
