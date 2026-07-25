// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/versioning"
)

type DescribeSubCommand struct {
	cmd.CommandActioner
}

// Describe renders one consolidated view of a function's health (RFC-0017): the
// summary, status conditions, package/build status (with the build log surfaced
// on failure), and the pods currently backing it — replacing the
// getmeta/pods/package-info hop. Each section is sourced independently, so a
// section whose source is unavailable degrades to "<none>" rather than failing
// the whole view.
func Describe(input cli.Input) error {
	return (&DescribeSubCommand{}).do(input)
}

func (opts *DescribeSubCommand) do(input cli.Input) error {
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error resolving namespace: %w", err)
	}
	name := input.String(flagkey.FnName)
	ctx := input.Context()

	// RFC-0025: --version swaps the live view for the SNAPSHOT inspector of
	// one pinned FunctionVersion entirely -- it does not layer on top of the
	// live sections above.
	if versionName := input.String(flagkey.FnDescribeVersion); versionName != "" {
		return opts.doVersion(ctx, namespace, name, versionName)
	}

	fn, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting function %s: %w", name, err)
	}

	describeFunctionTo(os.Stdout, fn, opts.packageFor(ctx, fn), opts.podsFor(ctx, fn),
		opts.versionsFor(ctx, namespace, name), opts.aliasesFor(ctx, namespace, name))
	return nil
}

// doVersion renders the RFC-0025 SNAPSHOT inspector for one FunctionVersion.
// Preflight (existence + Spec.FunctionName == name) is the shared
// versionref.go helper `fn test`/`fn pods`/`fn logs` already use for their
// own --version flags, so a typo'd --version surfaces the same clear error
// here.
func (opts *DescribeSubCommand) doVersion(ctx context.Context, namespace, name, versionName string) error {
	version, err := getOwnedFunctionVersion(ctx, opts.Client(), namespace, name, versionName)
	if err != nil {
		return err
	}

	aliases := opts.aliasesFor(ctx, namespace, name)
	env := opts.snapshotEnvFor(ctx, namespace, version)

	describeVersionTo(os.Stdout, version, env, aliases)
	return nil
}

// packageFor fetches the function's package, best-effort: an unreferenced or
// unreadable package renders as unavailable rather than failing the view.
func (opts *DescribeSubCommand) packageFor(ctx context.Context, fn *fv1.Function) *fv1.Package {
	ref := fn.Spec.Package.PackageRef
	if ref.Name == "" {
		return nil
	}
	pkg, err := opts.Client().FissionClientSet.CoreV1().Packages(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	return pkg
}

// podsFor lists the pods backing the function (same label selector as
// `function pods`), best-effort.
func (opts *DescribeSubCommand) podsFor(ctx context.Context, fn *fv1.Function) []corev1.Pod {
	selector := labels.Set{fv1.FUNCTION_NAME: fn.Name}
	if fn.Namespace != "" {
		selector[fv1.FUNCTION_NAMESPACE] = fn.Namespace
	}
	list, err := opts.Client().KubernetesClient.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		LabelSelector: selector.AsSelector().String(),
	})
	if err != nil {
		return nil
	}
	return list.Items
}

// versionsFor lists fnName's FunctionVersions via the ownership label
// selector (the same selector `fn versions` uses), best-effort: an
// unreadable list renders the VERSIONING section's count as 0 rather than
// failing the whole view, the same degrade-on-error convention as
// packageFor/podsFor above.
func (opts *DescribeSubCommand) versionsFor(ctx context.Context, namespace, fnName string) []fv1.FunctionVersion {
	selector := labels.SelectorFromSet(labels.Set{fv1.VersionFunctionNameLabel: fnName}).String()
	list, err := opts.Client().FissionClientSet.CoreV1().FunctionVersions(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil
	}
	return list.Items
}

// aliasesFor lists fnName's FunctionAliases, filtered client-side on
// Spec.FunctionName -- mirrors functionalias/list.go's filterByFunction: an
// alias created before the ownership label existed (or by hand) would be
// silently dropped by a selector-based List. Best-effort, same convention as
// versionsFor.
func (opts *DescribeSubCommand) aliasesFor(ctx context.Context, namespace, fnName string) []fv1.FunctionAlias {
	list, err := opts.Client().FissionClientSet.CoreV1().FunctionAliases(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	out := make([]fv1.FunctionAlias, 0, len(list.Items))
	for _, a := range list.Items {
		if a.Spec.FunctionName == fnName {
			out = append(out, a)
		}
	}
	return out
}

// snapshotEnvFor fetches the live Environment a FunctionVersion's snapshot
// recorded (Spec.Snapshot.Environment), best-effort: it exists only to
// compute EnvDrift for the --version SNAPSHOT inspector, so an unreadable or
// unset Environment renders drift as unassessable (nil) rather than failing
// the view -- mirrors envDriftByVersion's (versions.go) namespace-fallback
// and "absence means not assessable" conventions.
func (opts *DescribeSubCommand) snapshotEnvFor(ctx context.Context, namespace string, version *fv1.FunctionVersion) *fv1.Environment {
	ref := version.Spec.Snapshot.Environment
	if ref.Name == "" {
		return nil
	}
	envNS := ref.Namespace
	if envNS == "" {
		envNS = namespace
	}
	env, err := opts.Client().FissionClientSet.CoreV1().Environments(envNS).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	return env
}

func describeFunctionTo(out io.Writer, fn *fv1.Function, pkg *fv1.Package, pods []corev1.Pod, versions []fv1.FunctionVersion, aliases []fv1.FunctionAlias) {
	// Filter to the non-terminating pods once; the invocability headline and the
	// pods table both render from this same set so they cannot disagree.
	active := activePods(pods)

	w := util.NewTabWriter(out)
	fmt.Fprintf(w, "Name:\t%s\n", fn.Name)
	fmt.Fprintf(w, "Namespace:\t%s\n", fn.Namespace)
	fmt.Fprintf(w, "Environment:\t%s\n", environmentRef(fn.Spec.Environment))
	fmt.Fprintf(w, "Executor:\t%s\n", valueOr(string(fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType)))
	fmt.Fprintf(w, "Package:\t%s\n", valueOr(fn.Spec.Package.PackageRef.Name))
	fmt.Fprintf(w, "Invocable:\t%s\n", invocability(fn, active))
	fmt.Fprintf(w, "Created:\t%s\n", util.AgeOf(fn.CreationTimestamp))
	if line := kvLine(fn.Labels); line != "" {
		fmt.Fprintf(w, "Labels:\t%s\n", line)
	}
	w.Flush()

	util.PrintConditionsTo(out, fn.Status.Conditions)

	fmt.Fprintln(out, "\nPACKAGE:")
	describePackageTo(out, pkg)

	fmt.Fprintln(out, "\nPODS:")
	describePodsTo(out, active)

	fmt.Fprintln(out, "\nVERSIONING:")
	describeVersioningTo(out, fn, versions, aliases)
}

// activePods returns the non-terminating pods, the set both the invocability
// headline and the pods table render from.
func activePods(pods []corev1.Pod) []*corev1.Pod {
	active := make([]*corev1.Pod, 0, len(pods))
	for i := range pods {
		if pods[i].DeletionTimestamp == nil {
			active = append(active, &pods[i])
		}
	}
	return active
}

func describePackageTo(out io.Writer, pkg *fv1.Package) {
	if pkg == nil {
		fmt.Fprintf(out, "  %s\n", util.NoneValue)
		return
	}
	w := util.NewTabWriter(out)
	fmt.Fprintf(w, "Name:\t%s\n", pkg.Name)
	fmt.Fprintf(w, "Build Status:\t%s\n", valueOr(string(pkg.Status.BuildStatus)))
	w.Flush()
	util.PrintConditionsTo(out, pkg.Status.Conditions)
	// Surface the build log only on a failed build — that is when it is
	// actionable, and it keeps the healthy-path view compact.
	if pkg.Status.BuildStatus == fv1.BuildStatusFailed && pkg.Status.BuildLog != "" {
		fmt.Fprintf(out, "Build Logs:\n%s\n", strings.ReplaceAll(pkg.Status.BuildLog, `\n`, "\n"))
	}
}

func describePodsTo(out io.Writer, active []*corev1.Pod) {
	if len(active) == 0 {
		fmt.Fprintf(out, "  %s\n", util.NoneValue)
		return
	}
	printFunctionPodsTo(out, active)
}

// describeVersioningTo renders the RFC-0025 VERSIONING section: the
// mode/retain config, the version count (versionsFor is best-effort, so an
// unreadable list degrades to 0 rather than failing the view), and a mini
// alias table. A function that never opted into versioning and has no
// orphaned versions/aliases left over (e.g. from a prior opt-in since turned
// off) renders as a single "disabled" line instead of an otherwise-empty
// section.
func describeVersioningTo(out io.Writer, fn *fv1.Function, versions []fv1.FunctionVersion, aliases []fv1.FunctionAlias) {
	if fn.Spec.Versioning == nil && len(versions) == 0 && len(aliases) == 0 {
		fmt.Fprintf(out, "  Versioning: disabled\n")
		return
	}

	w := util.NewTabWriter(out)
	fmt.Fprintf(w, "Versioning:\t%s\n", versioningModeLine(fn.Spec.Versioning))
	fmt.Fprintf(w, "Versions:\t%d\n", len(versions))
	w.Flush()

	describeAliasesTableTo(out, aliases)
}

// versioningModeLine renders "mode=<auto|manual> retain=<n>" for a function
// with an active VersioningConfig (Mode defaults to auto, mirroring the CRD's
// kubebuilder default, in case a legacy or hand-edited spec left it unset;
// Retain defaults to versioning.DefaultRetain, mirroring the GC sweep's own
// fallback in retentiongc.go), or "disabled" for one whose VersioningConfig
// has been removed but that still has orphaned versions/aliases worth
// showing (see describeVersioningTo's zero-value branch above).
func versioningModeLine(cfg *fv1.VersioningConfig) string {
	if cfg == nil {
		return "disabled"
	}
	mode := cfg.Mode
	if mode == "" {
		mode = fv1.VersioningModeAuto
	}
	retain := versioning.DefaultRetain
	if cfg.Retain != nil {
		retain = *cfg.Retain
	}
	return fmt.Sprintf("mode=%s retain=%d", mode, retain)
}

// describeAliasesTableTo renders the NAME/TARGET/WEIGHT/ENVDRIFT mini table
// shared by the VERSIONING section (all of a function's aliases) and the
// --version SNAPSHOT inspector's ALIASED-BY table (aliases already filtered
// to those targeting one version). TARGET is aliasEffectiveTarget
// (versionref.go); ENVDRIFT reads the EnvDrift condition the
// AliasReconciler writes, "-" when absent -- the condition's own "removed,
// not False, when not assessable" contract (see
// FunctionAliasConditionEnvDrift), condensed to a table cell.
func describeAliasesTableTo(out io.Writer, aliases []fv1.FunctionAlias) {
	if len(aliases) == 0 {
		fmt.Fprintf(out, "  %s\n", util.NoneValue)
		return
	}
	w := util.NewTabWriter(out)
	fmt.Fprintln(w, "NAME\tTARGET\tWEIGHT\tENVDRIFT")
	for _, a := range aliases {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.Name, valueOr(aliasEffectiveTarget(&a)), aliasWeight(&a), aliasEnvDriftStatus(&a))
	}
	w.Flush()
}

// aliasWeight renders a's primary-target weight, util.NoneValue when unset
// (nil means 100%, per FunctionAliasSpec.Weight's doc comment).
func aliasWeight(a *fv1.FunctionAlias) string {
	if a.Spec.Weight == nil {
		return util.NoneValue
	}
	return fmt.Sprintf("%d", *a.Spec.Weight)
}

// aliasEnvDriftStatus reads a's EnvDrift condition status, "-" when the
// condition is absent (not assessable).
func aliasEnvDriftStatus(a *fv1.FunctionAlias) string {
	c := conditions.Find(a.Status.Conditions, fv1.FunctionAliasConditionEnvDrift)
	if c == nil {
		return "-"
	}
	return string(c.Status)
}

// describeVersionTo renders the RFC-0025 SNAPSHOT inspector for one
// FunctionVersion (`fission fn describe --version <name>`), replacing the
// live view entirely. env is the version's Spec.Snapshot.Environment fetched
// live, best-effort (nil when unreadable or unset -- see snapshotEnvFor), to
// compute EnvDrift against the environment's current Generation. aliases is
// fn's full, unfiltered alias list (as from aliasesFor); ALIASED-BY narrows
// it to those whose effective target is this version.
func describeVersionTo(out io.Writer, version *fv1.FunctionVersion, env *fv1.Environment, aliases []fv1.FunctionAlias) {
	w := util.NewTabWriter(out)
	fmt.Fprintf(w, "Name:\t%s\n", version.Name)
	fmt.Fprintf(w, "Function:\t%s\n", version.Spec.FunctionName)
	fmt.Fprintf(w, "Sequence:\t%d\n", version.Spec.Sequence)
	fmt.Fprintf(w, "Digest:\t%s\n", valueOr(version.Spec.PackageDigest))
	fmt.Fprintf(w, "Description:\t%s\n", versionDescription(version))
	fmt.Fprintf(w, "Published:\t%s\n", versionPublishedAt(version))
	fmt.Fprintf(w, "Age:\t%s\n", util.AgeOf(version.CreationTimestamp))
	fmt.Fprintf(w, "Entrypoint:\t%s\n", valueOr(version.Spec.Snapshot.Package.FunctionName))
	fmt.Fprintf(w, "Environment:\t%s\n", environmentRef(version.Spec.Snapshot.Environment))
	fmt.Fprintf(w, "Env Observed Generation:\t%d\n", version.Spec.EnvObservedGeneration)
	fmt.Fprintf(w, "Env Runtime Image:\t%s\n", valueOr(version.Spec.EnvRuntimeImage))
	fmt.Fprintf(w, "Env Drift:\t%s\n", versionEnvDrift(version, env))
	w.Flush()

	fmt.Fprintln(out, "\nALIASED-BY:")
	describeAliasesTableTo(out, aliasedByVersion(aliases, version.Name))
}

// versionDescription renders the caller-supplied publish description
// (versioning.DescriptionAnnotation), "-" when unset.
func versionDescription(version *fv1.FunctionVersion) string {
	if d := version.Annotations[versioning.DescriptionAnnotation]; d != "" {
		return d
	}
	return "-"
}

// versionPublishedAt renders Spec.PublishedAt, util.NoneValue on the zero
// value (defensive: every version minted by versioning.Publish sets it, but
// a hand-crafted or malformed CR should not panic on time.Format).
func versionPublishedAt(version *fv1.FunctionVersion) string {
	if version.Spec.PublishedAt.IsZero() {
		return util.NoneValue
	}
	return version.Spec.PublishedAt.Format(time.RFC3339)
}

// versionEnvDrift compares the version's publish-time environment
// observation (Spec.EnvObservedGeneration) against the live Environment's
// current Generation: "DRIFT (live generation=<n>)" when they differ,
// "current" when they still match, or util.NoneValue when env is nil (the
// snapshot recorded no Environment at all, or the live one is unreadable) --
// not assessable, mirroring envDriftByVersion's (versions.go) and
// FunctionAliasConditionEnvDrift's "absence means cannot tell" convention.
func versionEnvDrift(version *fv1.FunctionVersion, env *fv1.Environment) string {
	if env == nil {
		return util.NoneValue
	}
	if version.Spec.EnvObservedGeneration != env.Generation {
		return fmt.Sprintf("DRIFT (live generation=%d)", env.Generation)
	}
	return "current"
}

// aliasedByVersion filters aliases to those whose effective target
// (aliasEffectiveTarget, versionref.go) is versionName.
func aliasedByVersion(aliases []fv1.FunctionAlias, versionName string) []fv1.FunctionAlias {
	out := make([]fv1.FunctionAlias, 0, len(aliases))
	for _, a := range aliases {
		if aliasEffectiveTarget(&a) == versionName {
			out = append(out, a)
		}
	}
	return out
}

// invocability answers "can I call this right now, and if not, why?" from the
// data describe already has — the Ready condition and the count of fully-ready
// pods — so it needs no executor diagnostics endpoint. A Ready function with no
// warm pod is still invocable (it cold-starts), which is called out.
func invocability(fn *fv1.Function, active []*corev1.Pod) string {
	warm, serving := 0, 0
	for _, pod := range active {
		ready, total := utils.PodContainerReadyStatus(pod)
		if total == 0 || ready != total {
			continue
		}
		warm++
		// fission.io/served=true means the pod is published to its function's
		// EndpointSlice and actually serving traffic (RFC-0002 data plane).
		if pod.Labels[fv1.SERVED_LABEL] == fv1.SERVED_VALUE {
			serving++
		}
	}
	switch util.ConditionStatus(fn.Status.Conditions, fv1.FunctionConditionReady) {
	case string(metav1.ConditionTrue):
		switch {
		case serving > 0:
			return fmt.Sprintf("Yes (%d of %d warm pod(s) serving)", serving, warm)
		case warm > 0:
			return fmt.Sprintf("Yes (%d warm pod(s))", warm)
		default:
			return "Yes (cold start on first call)"
		}
	case string(metav1.ConditionFalse):
		return "No - function not Ready (see CONDITIONS)"
	default:
		return util.NoneValue
	}
}

// environmentRef renders the environment reference, qualifying with the
// namespace only when it is set and non-default.
func environmentRef(e fv1.EnvironmentReference) string {
	if e.Name == "" {
		return util.NoneValue
	}
	if e.Namespace != "" && e.Namespace != metav1.NamespaceDefault {
		return fmt.Sprintf("%s (%s)", e.Name, e.Namespace)
	}
	return e.Name
}

// kvLine renders a label/annotation map as a stable, comma-separated string.
func kvLine(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

func valueOr(s string) string {
	if s == "" {
		return util.NoneValue
	}
	return s
}
