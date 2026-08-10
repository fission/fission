// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package _package

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

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

const buildWatchPollInterval = time.Second

// buildWatchMaxConsecutiveGetErrors bounds how long the status poll tolerates
// consecutive non-NotFound API errors before surfacing the last one. Without
// it a persistent failure (expired token, RBAC deny) combined with the
// default no-timeout watch would hang forever with zero output.
const buildWatchMaxConsecutiveGetErrors = 5

// WatchPackageBuild is the cli.Input-facing wrapper around watchPackageBuild:
// it applies --timeout only when positive (flag.PkgWatchTimeout defaults to 0
// — a build has no natural upper bound, so the default is to wait
// indefinitely, like `kubectl logs -f`) and writes to the input's stdout.
func WatchPackageBuild(input cli.Input, client cmd.Client, namespace, name string) error {
	ctx := input.Context()
	if timeout := input.Duration(flagkey.WaitTimeout); timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return watchPackageBuild(ctx, client, input.Stdout(), namespace, name)
}

// watchPackageBuild follows the build of the package at namespace/name to a
// terminal state. The guarantee is the status poll: it decides completion,
// prints the final Status.BuildLog (the source of truth), and returns non-nil
// on a failed build so the command exits non-zero. Streaming live lines from
// the environment's builder pod is strictly best-effort decoration on top —
// any failure there (builder pod not up yet, rolled mid-build, RBAC denying a
// cross-namespace pod list) silently degrades to "wait and print the final
// log", never to a hang or a spurious command failure.
func watchPackageBuild(ctx context.Context, client cmd.Client, out io.Writer, namespace, name string) error {
	// Fail fast on an environment that can never run this build — otherwise a
	// source package against a builder-less env stays pending and a default
	// (no-timeout) watch would wait forever.
	if err := refuseBuilderlessEnv(ctx, client, namespace, name); err != nil {
		return err
	}

	lw := &lockedWriter{w: out}

	streamCtx, stopStream := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() {
		streamBuilderLogs(streamCtx, client, lw, namespace, name)
	})
	// Stop the log stream before the final build-log print regardless of how
	// the poll ends, so stream lines can't interleave into the final report.
	defer wg.Wait()
	defer stopStream()

	var pkg *fv1.Package
	// Sentinel that matches no real status, so the very first poll announces
	// the current state.
	lastStatus := fv1.BuildStatus("\x00")
	consecutiveGetErrors := 0
	check := func(ctx context.Context) (bool, error) {
		p, err := client.FissionClientSet.CoreV1().Packages(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if util.IsNotFound(err) {
				// The caller just created/updated this package; NotFound now
				// means it was deleted underneath the watch.
				return false, fmt.Errorf("package %s/%s no longer exists: %w", namespace, name, err)
			}
			// Tolerate a transient API blip, but a persistent failure
			// (expired token, RBAC deny) must surface rather than spin
			// silently forever under the default no-timeout watch.
			consecutiveGetErrors++
			if consecutiveGetErrors >= buildWatchMaxConsecutiveGetErrors {
				return false, fmt.Errorf("getting package %s/%s repeatedly failed while watching the build: %w", namespace, name, err)
			}
			return false, nil
		}
		consecutiveGetErrors = 0
		pkg = p
		// An empty status is a fresh create the buildermgr hasn't classified
		// yet (the /status subresource strips client-set status); to the user
		// that is the same as pending, including for the breadcrumb dedupe.
		st := p.Status.BuildStatus
		if st == "" {
			st = fv1.BuildStatusPending
		}
		if st != lastStatus {
			lastStatus = st
			switch st {
			case fv1.BuildStatusPending:
				fmt.Fprintf(lw, "Waiting for package '%v' build to start...\n", name)
			case fv1.BuildStatusRunning:
				fmt.Fprintf(lw, "Package '%v' build running...\n", name)
			}
		}
		switch st {
		case fv1.BuildStatusSucceeded, fv1.BuildStatusFailed, fv1.BuildStatusNone:
			return true, nil
		}
		return false, nil
	}

	if err := util.PollUntil(ctx, buildWatchPollInterval, check); err != nil {
		if util.PollEnded(err) {
			return fmt.Errorf("%s waiting for package %s/%s build to finish: %w",
				util.PollDeadlineVerb(ctx), namespace, name, ctx.Err())
		}
		return err
	}

	// Silence the live stream before printing the authoritative report.
	stopStream()
	wg.Wait()

	switch pkg.Status.BuildStatus {
	case fv1.BuildStatusNone:
		fmt.Fprintf(out, "Package '%v' has nothing to build\n", name)
		return nil
	case fv1.BuildStatusFailed:
		printFinalBuildLog(out, pkg)
		return fmt.Errorf("package %s/%s build failed", namespace, name)
	default: // BuildStatusSucceeded
		printFinalBuildLog(out, pkg)
		fmt.Fprintf(out, "Package '%v' build succeeded\n", name)
		return nil
	}
}

// printFinalBuildLog prints Status.BuildLog — the authoritative build record
// (the live stream is best-effort and, on a shared builder pod, may interleave
// concurrent builds; this is only this build's output).
func printFinalBuildLog(out io.Writer, pkg *fv1.Package) {
	log := pkg.Status.BuildLog
	if strings.TrimSpace(log) == "" {
		return
	}
	if !strings.HasSuffix(log, "\n") {
		log += "\n"
	}
	fmt.Fprintf(out, "\n========= build log =========\n%s========= end build log =========\n", log)
}

// refuseBuilderlessEnv returns an error when the package has a source archive
// to build but its Environment can never build it (no builder image, or a v1
// env — the same predicate EnvironmentReconciler uses to skip builder
// creation). Such a package stays pending forever, so a no-timeout watch
// must refuse up front rather than hang. Lookup failures (RBAC, env not yet
// created) are NOT terminal — the watch proceeds and the status decides.
func refuseBuilderlessEnv(ctx context.Context, client cmd.Client, namespace, name string) error {
	pkg, err := client.FissionClientSet.CoreV1().Packages(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil || pkg.Spec.Source.IsEmpty() {
		return nil
	}
	switch pkg.Status.BuildStatus {
	case "", fv1.BuildStatusPending:
		// Only a build that has not started can wait forever on a missing
		// builder; running/terminal statuses prove a builder exists(ed).
	default:
		return nil
	}
	envNamespace := pkg.Spec.Environment.Namespace
	if envNamespace == "" {
		envNamespace = namespace
	}
	env, err := client.FissionClientSet.CoreV1().Environments(envNamespace).Get(ctx, pkg.Spec.Environment.Name, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	if env.Spec.Version == 1 || len(env.Spec.Builder.Image) == 0 {
		return fmt.Errorf("environment %s/%s has no builder (builder image unset or v1 env): the source package can never be built, so there is no build to watch",
			envNamespace, env.Name)
	}
	return nil
}

// streamBuilderLogs tails the builder container of the package's environment
// builder pod onto out until ctx is cancelled. Purely best-effort: every
// failure path retries after a poll interval and nothing is ever reported as
// an error. SinceTime advances across reconnects so a re-established stream
// does not re-dump lines already printed.
func streamBuilderLogs(ctx context.Context, client cmd.Client, out io.Writer, namespace, name string) {
	since := metav1.Now()
	for {
		if pod := findBuilderPod(ctx, client, namespace, name); pod != nil {
			sinceCopy := since
			opts := &corev1.PodLogOptions{
				Container: fv1.BuilderContainerName,
				Follow:    true,
				SinceTime: &sinceCopy,
			}
			stream, err := client.KubernetesClient.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, opts).Stream(ctx)
			if err == nil {
				copyLogLines(out, stream)
				stream.Close()
				// The stream ended (pod rolled, kubelet rotation, ...); on
				// reconnect only fetch lines newer than what we've shown.
				since = metav1.Now()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(buildWatchPollInterval):
		}
	}
}

// copyLogLines copies whole lines from the log stream to out (which serializes
// writes), so stream lines and status breadcrumbs never interleave mid-line.
// bufio.Reader rather than Scanner: an oversized log line must not abort the
// follow (same rationale as logdb.followContainer).
func copyLogLines(out io.Writer, stream io.Reader) {
	r := bufio.NewReader(stream)
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			if !strings.HasSuffix(line, "\n") {
				line += "\n"
			}
			if _, werr := io.WriteString(out, line); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// findBuilderPod locates the newest ready builder pod for the package's
// environment, or nil if none can be found right now. Primary lookup is in
// the Environment's own namespace. When that yields nothing AND the env lives
// in `default` — the only namespace the server's GetBuilderNS remap moves
// builder pods out of (pkg/utils/namespace.go) — it falls back to a
// cluster-wide list; because the builder labels carry no env-namespace
// identity, that fallback additionally requires the envResourceVersion label
// to match the live Environment, so a same-named env in another namespace
// can't be picked. All lookups may fail for RBAC or availability reasons —
// that only degrades the live stream, so errors just yield nil.
func findBuilderPod(ctx context.Context, client cmd.Client, namespace, name string) *corev1.Pod {
	pkg, err := client.FissionClientSet.CoreV1().Packages(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	envName := pkg.Spec.Environment.Name
	envNamespace := pkg.Spec.Environment.Namespace
	if envNamespace == "" {
		envNamespace = namespace
	}

	// In the primary (namespace-scoped) lookup the Environment's current
	// ResourceVersion is only a preference, never a requirement: the label
	// lags behind env edits until the new builder generation rolls out.
	var envResourceVersion string
	if env, err := client.FissionClientSet.CoreV1().Environments(envNamespace).Get(ctx, envName, metav1.GetOptions{}); err == nil {
		envResourceVersion = env.ResourceVersion
	}

	selector := labels.Set{
		fv1.BuilderLabelOwner:   fv1.BuilderOwnerBuilderMgr,
		fv1.BuilderLabelEnvName: envName,
	}.AsSelector().String()

	podList, err := client.KubernetesClient.CoreV1().Pods(envNamespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err == nil && len(podList.Items) > 0 {
		return pickBuilderPod(podList.Items, envResourceVersion)
	}
	if envNamespace != metav1.NamespaceDefault || envResourceVersion == "" {
		return nil
	}
	podList, err = client.KubernetesClient.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil
	}
	candidates := podList.Items[:0]
	for i := range podList.Items {
		if podList.Items[i].Labels[fv1.BuilderLabelEnvResourceVersion] == envResourceVersion {
			candidates = append(candidates, podList.Items[i])
		}
	}
	return pickBuilderPod(candidates, envResourceVersion)
}

// pickBuilderPod orders candidate builder pods by (ready, matches current env
// ResourceVersion, newest) and returns the best one.
func pickBuilderPod(pods []corev1.Pod, envResourceVersion string) *corev1.Pod {
	if len(pods) == 0 {
		return nil
	}
	rank := func(p *corev1.Pod) (ready, rvMatch bool) {
		ready = utils.IsReadyPod(p)
		rvMatch = envResourceVersion != "" && p.Labels[fv1.BuilderLabelEnvResourceVersion] == envResourceVersion
		return ready, rvMatch
	}
	sort.SliceStable(pods, func(i, j int) bool {
		iReady, iRV := rank(&pods[i])
		jReady, jRV := rank(&pods[j])
		if iReady != jReady {
			return iReady
		}
		if iRV != jRV {
			return iRV
		}
		return pods[j].CreationTimestamp.Before(&pods[i].CreationTimestamp)
	})
	return &pods[0]
}

// lockedWriter serializes concurrent writes (status breadcrumbs vs. streamed
// log lines) onto one writer, same as logdb's.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
