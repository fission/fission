// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// The env runtime contract (mirrors the in-cluster fetcher/specialize layout):
// the deploy package is mounted under localMountPath, the runtime listens on
// envContainerPort for both /v2/specialize and invocation, and the v2 target
// file is "deployarchive" (v1 is "user"). Container-executor functions bring
// their own server image and listen on a function-defined port instead.
const (
	localMountPath       = "/userfunc"
	envContainerPort     = 8888
	targetFilenameDeploy = "deployarchive" // v2
	targetFilenameUser   = "user"          // v1

	// fissionFunctionHeaderPrefix mirrors the router's HEADERS_FISSION_FUNCTION_PREFIX
	// (pkg/router/requesthHeader.go); the env runtime exposes these to user code as
	// the invocation's function context.
	fissionFunctionHeaderPrefix = "X-Fission-Function"
)

// functionHeaders renders the X-Fission-Function-* metadata headers the router
// attaches in-cluster (setFunctionMetadataToHeader), in the "Key:Value" form
// DoHTTPRequest consumes — so a local invocation carries the same function
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
	// Stop stops and removes the container.
	Stop(ctx context.Context, id string) error
	// Close releases the engine client.
	Close() error
}

// containerSpec is the minimal description of the container `run-local` starts:
// the image, an optional host directory bind-mounted as the userfunc volume
// (empty for container-executor functions, which carry their own code), the
// host port published to ContainerPort, the in-container port the server
// listens on, and extra env vars.
type containerSpec struct {
	Image         string
	HostDir       string
	HostPort      int
	ContainerPort int
	Env           []string
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
		return fmt.Sprintf("http://127.0.0.1:%d/v2/specialize", hostPort)
	}
	return fmt.Sprintf("http://127.0.0.1:%d/specialize", hostPort)
}

// freePort asks the OS for an unused localhost TCP port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// prepareSourceMount lays a single source file out as the runtime expects under
// a fresh temp dir: <dir>/<targetFilename>. Returns the host directory to mount.
func prepareSourceMount(codePath string, envVersion int) (string, error) {
	dir, err := os.MkdirTemp("", "fission-run-")
	if err != nil {
		return "", fmt.Errorf("creating local mount dir: %w", err)
	}
	src, err := os.ReadFile(codePath)
	if err != nil {
		return "", fmt.Errorf("reading source %q: %w", codePath, err)
	}
	dst := filepath.Join(dir, targetFilename(envVersion))
	if err := os.WriteFile(dst, src, 0o644); err != nil {
		return "", fmt.Errorf("writing source into mount: %w", err)
	}
	return dir, nil
}

// dockerRuntime is the moby/moby/client-backed localRuntime.
type dockerRuntime struct {
	cli    *client.Client
	logger logr.Logger
}

func newDockerRuntime(logger logr.Logger) (*dockerRuntime, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("connecting to the Docker engine (is it running?): %w", err)
	}
	return &dockerRuntime{cli: cli, logger: logger}, nil
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
	if err := resp.Wait(ctx); err != nil {
		d.logger.V(1).Info("image pull stream reported an error; assuming it is present locally", "image", image, "error", err.Error())
	}
	return nil
}

func (d *dockerRuntime) StartContainer(ctx context.Context, spec containerSpec) (string, error) {
	if spec.ContainerPort < 1 || spec.ContainerPort > 65535 {
		return "", fmt.Errorf("invalid container port %d", spec.ContainerPort)
	}
	port, ok := network.PortFrom(uint16(spec.ContainerPort), network.TCP)
	if !ok {
		return "", fmt.Errorf("invalid container port %d", spec.ContainerPort)
	}
	hostConfig := &container.HostConfig{
		PortBindings: network.PortMap{port: []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: strconv.Itoa(spec.HostPort)}}},
	}
	// Container-executor functions carry their own code in the image; only env
	// runtimes need the userfunc bind mount.
	if spec.HostDir != "" {
		hostConfig.Binds = []string{spec.HostDir + ":" + localMountPath}
	}
	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:        spec.Image,
			Env:          spec.Env,
			ExposedPorts: network.PortSet{port: struct{}{}},
		},
		HostConfig: hostConfig,
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
