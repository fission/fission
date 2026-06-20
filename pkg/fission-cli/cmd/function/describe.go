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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/utils"
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
	_, namespace, err := opts.GetResourceNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return fmt.Errorf("error resolving namespace: %w", err)
	}
	name := input.String(flagkey.FnName)
	ctx := input.Context()

	fn, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting function %s: %w", name, err)
	}

	describeFunctionTo(os.Stdout, fn, opts.packageFor(ctx, fn), opts.podsFor(ctx, fn))
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

func describeFunctionTo(out io.Writer, fn *fv1.Function, pkg *fv1.Package, pods []corev1.Pod) {
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
