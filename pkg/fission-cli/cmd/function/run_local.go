// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/fatih/color"
	"github.com/go-logr/logr"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/svcinfo"
)

// The env runtime contract (mirrors the in-cluster fetcher/specialize layout):
// the deploy package is mounted under localMountPath, the runtime listens on
// envContainerPort for both /v2/specialize and invocation, and the v2 target
// file is "deployarchive" (v1 is "user"). Container-executor functions bring
// their own server image and listen on a function-defined port instead.
const (
	// localhostAddr is the loopback address run-local publishes container ports on
	// and dials; everything stays on the developer's machine.
	localhostAddr = "127.0.0.1"

	localMountPath       = "/userfunc"
	secretsMountPath     = "/secrets" // /secrets/<ns>/<name>/<key>, matching the fetcher
	configsMountPath     = "/configs" // /configs/<ns>/<name>/<key>, matching the fetcher
	envContainerPort     = svcinfo.PortEnvRuntime
	targetFilenameDeploy = "deployarchive" // v2
	targetFilenameUser   = "user"          // v1

	// fissionFunctionHeaderPrefix mirrors the router's HEADERS_FISSION_FUNCTION_PREFIX
	// (pkg/router/requesthHeader.go); the env runtime exposes these to user code as
	// the invocation's function context.
	fissionFunctionHeaderPrefix = "X-Fission-Function"
)

// functionHeaders renders the X-Fission-Function-* metadata headers the router
// attaches in-cluster (setFunctionMetadataToHeader), in the "Key:Value" form
// combinedHTTPRequest consumes — so a local invocation carries the same function
// context as an in-cluster one.
func functionHeaders(meta metav1.ObjectMeta) []string {
	return []string{
		fmt.Sprintf("%s-Uid:%s", fissionFunctionHeaderPrefix, meta.UID),
		fmt.Sprintf("%s-Name:%s", fissionFunctionHeaderPrefix, meta.Name),
		fmt.Sprintf("%s-Namespace:%s", fissionFunctionHeaderPrefix, meta.Namespace),
		fmt.Sprintf("%s-ResourceVersion:%s", fissionFunctionHeaderPrefix, meta.ResourceVersion),
	}
}

// localRuntime abstracts the container engine so the run flow stays unit-testable
// behind a fake; the Docker implementation is the only production backend.
type localRuntime interface {
	// PullImage fetches the image if not present locally (best-effort).
	PullImage(ctx context.Context, image string) error
	// StartContainer creates and starts the env runtime container and returns its id.
	StartContainer(ctx context.Context, spec containerSpec) (string, error)
	// Logs returns the container's recent (multiplexed) log stream.
	Logs(ctx context.Context, id string) (io.ReadCloser, error)
	// Stop stops and removes the container.
	Stop(ctx context.Context, id string) error
	// Close releases the engine client.
	Close() error
}

// bindMount is one host-dir → container-dir bind mount.
type bindMount struct {
	HostDir      string
	ContainerDir string
}

// portMapping publishes a container port on a host port.
type portMapping struct {
	Host      int
	Container int
}

// containerSpec describes a container `run-local` starts: the image, its bind
// mounts (userfunc code, secrets, configmaps), the ports published to the host
// (runtime/invoke port plus optional debug port), extra env vars, and an
// optional command override (the builder image's default CMD does not start its
// server — buildermgr runs "/builder <sharedPath>" — so the builder leg sets it).
type containerSpec struct {
	Image  string
	Mounts []bindMount
	Ports  []portMapping
	Env    []string
	Cmd    []string
}

// loadRequest is the wire shape of the env server's /v2/specialize body. It is
// kept local (rather than importing the heavy pkg/fetcher) so the fission CLI
// binary stays lean; TestLoadRequestWireContract guards it against drift from
// fetcher.FunctionLoadRequest.
type loadRequest struct {
	FilePath         string             `json:"filepath"`
	FunctionName     string             `json:"functionName"`
	URL              string             `json:"url"`
	FunctionMetadata *metav1.ObjectMeta `json:"FunctionMetadata"`
	EnvVersion       int                `json:"envVersion"`
}

// targetFilename is the name (under the mount) the runtime loads the function
// from: "deployarchive" for v2+, "user" for v1 — matching the in-cluster
// fetcher's TargetFilename.
func targetFilename(envVersion int) string {
	if envVersion >= 2 {
		return targetFilenameDeploy
	}
	return targetFilenameUser
}

// buildLoadRequest constructs the specialize load request for a local run,
// pointing FilePath at the bind-mounted source. Field semantics match the
// in-cluster fetcher's NewSpecializeRequest (asserted by a contract test):
// FunctionName carries the package entrypoint (fn.Spec.Package.FunctionName).
func buildLoadRequest(meta *metav1.ObjectMeta, entrypoint string, envVersion int) loadRequest {
	return loadRequest{
		FilePath:         filepath.Join(localMountPath, targetFilename(envVersion)),
		FunctionName:     entrypoint,
		FunctionMetadata: meta,
		EnvVersion:       envVersion,
	}
}

// specializeURL is the env server's specialize endpoint for the given version.
func specializeURL(hostPort, envVersion int) string {
	if envVersion >= 2 {
		return fmt.Sprintf("http://%s:%d/v2/specialize", localhostAddr, hostPort)
	}
	return fmt.Sprintf("http://%s:%d/specialize", localhostAddr, hostPort)
}

// stopQuietly tears down a container with a bounded, cancellation-immune context
// so teardown still runs after Ctrl-C (the container outlives ctx) but a wedged
// daemon can't hang CLI exit.
func stopQuietly(ctx context.Context, rt localRuntime, id string) {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_ = rt.Stop(stopCtx, id)
}

// dockerRuntime is the moby/moby/client-backed localRuntime.
type dockerRuntime struct {
	cli      *client.Client
	logger   logr.Logger
	progress io.Writer // pull progress (the developer's terminal)
}

func newDockerRuntime(logger logr.Logger) (*dockerRuntime, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("connecting to the Docker engine (is it running?): %w", err)
	}
	return &dockerRuntime{cli: cli, logger: logger, progress: os.Stderr}, nil
}

func (d *dockerRuntime) PullImage(ctx context.Context, image string) error {
	// Best-effort throughout: a locally-present image (e.g. a private/dev tag that
	// the registry won't serve) is fine — StartContainer is the authority on
	// whether the image is actually usable, so neither a failed pull request nor a
	// registry error mid-stream should abort the run.
	resp, err := d.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		d.logger.V(1).Info("image pull failed; assuming it is present locally", "image", image, "error", err.Error())
		return nil
	}
	defer resp.Close()
	streamPullProgress(ctx, resp, image, d.progress)
	return nil
}

// streamPullProgress prints a concise, throttled view of an image pull — a live
// "N/M layers" line on a terminal, a one-line start/end otherwise. A pull/stream
// error is treated as best-effort (the image may already be present locally).
func streamPullProgress(ctx context.Context, resp client.ImagePullResponse, image string, w io.Writer) {
	// The live "\r N/M layers" redraw only makes sense on a color-capable
	// terminal, which is exactly what colorEnabled answers (and it honors
	// NO_COLOR, so NO_COLOR=1 falls back to the plain one-line form).
	live := colorEnabled(w)
	step(w, "Pulling %s ...", image)

	complete := map[string]bool{}
	var lastPrint time.Time
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil || msg.Error != nil {
			return // best-effort: StartContainer is the authority on the image
		}
		if msg.ID == "" {
			continue // non-layer line ("Pulling from ...", digest, etc.)
		}
		complete[msg.ID] = complete[msg.ID] || msg.Status == "Pull complete" || msg.Status == "Already exists"
		if live && time.Since(lastPrint) > 200*time.Millisecond {
			fmt.Fprintf(w, "\r  %s", paint(w, color.FgCyan, fmt.Sprintf("%d/%d layers ", countTrue(complete), len(complete))))
			lastPrint = time.Now()
		}
	}
	if live {
		fmt.Fprintf(w, "\r  %s\n", paint(w, color.FgGreen, fmt.Sprintf("pulled %d layers          ", len(complete))))
	} else {
		step(w, "  pulled %d layers", len(complete))
	}
}

func countTrue(m map[string]bool) int {
	n := 0
	for _, v := range m {
		if v {
			n++
		}
	}
	return n
}

func (d *dockerRuntime) StartContainer(ctx context.Context, spec containerSpec) (string, error) {
	exposed := network.PortSet{}
	bindings := network.PortMap{}
	for _, p := range spec.Ports {
		if p.Container < 1 || p.Container > 65535 {
			return "", fmt.Errorf("invalid container port %d", p.Container)
		}
		port, ok := network.PortFrom(uint16(p.Container), network.TCP)
		if !ok {
			return "", fmt.Errorf("invalid container port %d", p.Container)
		}
		exposed[port] = struct{}{}
		bindings[port] = []network.PortBinding{{HostIP: netip.MustParseAddr(localhostAddr), HostPort: strconv.Itoa(p.Host)}}
	}

	binds := make([]string, 0, len(spec.Mounts))
	for _, m := range spec.Mounts {
		// Docker treats a non-absolute bind source as a named volume (silently
		// mounting an empty volume) rather than the host directory — enforce
		// absolute at the boundary so no caller can hit that trap.
		if !filepath.IsAbs(m.HostDir) {
			return "", fmt.Errorf("bind mount source %q must be an absolute path", m.HostDir)
		}
		binds = append(binds, m.HostDir+":"+m.ContainerDir)
	}

	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:        spec.Image,
			Env:          spec.Env,
			Cmd:          spec.Cmd,
			ExposedPorts: exposed,
		},
		HostConfig: &container.HostConfig{Binds: binds, PortBindings: bindings},
	})
	if err != nil {
		return "", fmt.Errorf("creating container: %w", err)
	}
	if _, err := d.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		// Reap the created-but-unstarted container so a failed start (e.g. a
		// host-port conflict from the freePort→start race) doesn't orphan it.
		_, _ = d.cli.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		return "", fmt.Errorf("starting container: %w", err)
	}
	return created.ID, nil
}

func (d *dockerRuntime) Logs(ctx context.Context, id string) (io.ReadCloser, error) {
	return d.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "40"})
}

func (d *dockerRuntime) Stop(ctx context.Context, id string) error {
	if _, err := d.cli.ContainerStop(ctx, id, client.ContainerStopOptions{}); err != nil {
		d.logger.V(1).Info("container stop failed", "id", id, "error", err.Error())
	}
	_, err := d.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
	return err
}

func (d *dockerRuntime) Close() error {
	return d.cli.Close()
}

// dumpContainerLogs best-effort writes the container's recent logs to w so a
// specialize/startup failure surfaces the env's actual error (e.g. an import
// traceback) instead of only the HTTP status. The stream is multiplexed
// stdout/stderr, demuxed via stdcopy. A short settle lets the env flush the
// error it logged around the failing response before we read.
func dumpContainerLogs(ctx context.Context, rt localRuntime, id string, w io.Writer) {
	select {
	case <-ctx.Done():
	case <-time.After(300 * time.Millisecond):
	}
	logs, err := rt.Logs(context.WithoutCancel(ctx), id)
	if err != nil {
		return
	}
	defer logs.Close()
	var buf bytes.Buffer
	if _, err := stdcopy.StdCopy(&buf, &buf, logs); err != nil || buf.Len() == 0 {
		return
	}
	note(w, "--- container logs ---")
	_, _ = io.Copy(w, &buf)
	note(w, "--- end container logs ---")
}
