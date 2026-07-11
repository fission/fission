// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fission/fission/pkg/utils"

	"github.com/fission/fission/pkg/svcinfo"
)

// The builder contract (pkg/builder, cmd/builder): the env builder image runs a
// server on 8001 with the shared volume at builderSharedPath; POST / a
// {srcPkgFilename, command} and it writes the compiled artifact under the shared
// volume and returns its filename.
const (
	builderSharedPath = "/packages"
	builderBinary     = "/builder" // builder server entrypoint (buildermgr runs "/builder /packages")
	builderPort       = svcinfo.PortBuilder
	builderSrcDirName = "src"
	// builderBuildTimeout bounds the synchronous build POST. A real build can take
	// minutes (Maven downloading its dependency tree on first run, a large Go/Rust
	// compile), so this is generous; the request is also cancellable via ctx
	// (Ctrl-C). The in-cluster builder imposes no comparable short cap.
	builderBuildTimeout = 15 * time.Minute
)

// buildRequest / buildResponse mirror pkg/builder.PackageBuild{Request,Response}.
// They are kept local so the CLI need not import the heavy pkg/builder; the
// fields are a stable wire contract.
type buildRequest struct {
	SrcPkgFilename string `json:"srcPkgFilename"`
	BuildCommand   string `json:"command"`
}

type buildResponse struct {
	ArtifactFilename string `json:"artifactFilename"`
	BuildLogs        string `json:"buildLogs"`
}

// runBuilder compiles cfg.codePath with the env's builder image, reproducing
// buildermgr's contract, and writes the resulting deploy artifact to dstDeploy
// (the runtime's /userfunc/deployarchive). It streams the build logs to stderr.
func runBuilder(ctx context.Context, rt localRuntime, cfg runConfig, dstDeploy string, stderr io.Writer) error {
	sharedDir, err := os.MkdirTemp("", "fission-build-")
	if err != nil {
		return fmt.Errorf("creating build dir: %w", err)
	}
	defer os.RemoveAll(sharedDir)

	// Lay the source out as a directory under the shared volume, the shape the
	// builder's SRC_PKG expects.
	srcDir := filepath.Join(sharedDir, builderSrcDirName)
	if err := copyTree(cfg.codePath, srcDir); err != nil {
		return fmt.Errorf("staging source: %w", err)
	}

	hostPort, err := utils.FindFreePort()
	if err != nil {
		return fmt.Errorf("finding a free port: %w", err)
	}

	if err := rt.PullImage(ctx, cfg.builderImage); err != nil {
		return fmt.Errorf("pulling builder image %q: %w", cfg.builderImage, err)
	}
	step(stderr, "Building with %s ...", cfg.builderImage)
	id, err := rt.StartContainer(ctx, containerSpec{
		Image:  cfg.builderImage,
		Mounts: []bindMount{{HostDir: sharedDir, ContainerDir: builderSharedPath}},
		Ports:  []portMapping{{Host: hostPort, Container: builderPort}},
		// The builder image's default CMD does not start the builder server;
		// run it the way buildermgr does ("/builder <sharedPath>").
		Cmd: []string{builderBinary, builderSharedPath},
	})
	if err != nil {
		return err
	}
	defer stopQuietly(ctx, rt, id)

	if err := waitForServer(ctx, hostPort); err != nil {
		dumpContainerLogs(ctx, rt, id, stderr)
		return fmt.Errorf("waiting for builder: %w", err)
	}

	resp, err := postBuild(ctx, hostPort, buildRequest{SrcPkgFilename: builderSrcDirName, BuildCommand: cfg.buildCommand})
	if err != nil {
		return err
	}
	if logs := resp.BuildLogs; logs != "" {
		fmt.Fprintln(stderr, logs)
	}
	if resp.ArtifactFilename == "" {
		return fmt.Errorf("builder returned no artifact")
	}

	// The artifact name comes from the builder container's response; validate it
	// stays under sharedDir (no traversal) before reading it.
	artifactPath, err := utils.RootJoin(sharedDir, resp.ArtifactFilename)
	if err != nil {
		return fmt.Errorf("builder returned an unsafe artifact path %q: %w", resp.ArtifactFilename, err)
	}
	if err := copyTree(artifactPath, dstDeploy); err != nil {
		return fmt.Errorf("collecting build artifact: %w", err)
	}
	return nil
}

// postBuild POSTs the build request to the builder and decodes its response.
func postBuild(ctx context.Context, hostPort int, req buildRequest) (buildResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return buildResponse{}, fmt.Errorf("encoding build request: %w", err)
	}
	url := fmt.Sprintf("http://%s:%d/", localhostAddr, hostPort)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return buildResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: builderBuildTimeout}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return buildResponse{}, fmt.Errorf("calling builder: %w", err)
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return buildResponse{}, fmt.Errorf("reading builder response: %w", err)
	}
	if httpResp.StatusCode >= 300 {
		return buildResponse{}, fmt.Errorf("build failed (%d): %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}
	var resp buildResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return buildResponse{}, fmt.Errorf("decoding builder response: %w", err)
	}
	return resp, nil
}

// copyTree copies src to dst, where src may be a single file or a directory
// tree. For a directory, reads are confined to src through an os.Root so a
// symlink inside the tree (e.g. one a malicious builder image planted in its
// artifact) cannot escape to read an arbitrary host file.
func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode())
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		target := filepath.Join(dst, p)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		in, err := root.Open(p) // os.Root refuses a symlink that escapes src
		if err != nil {
			return err
		}
		defer in.Close()
		return writeStream(in, target, fi.Mode())
	})
}

// copyFile copies a single file (the user's own --code; trusted) to dst.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeStream(in, dst, mode)
}

// writeStream streams in to dst, reducing mode to its permission bits so no
// setuid/setgid/sticky bit is carried from a copied source. Build artifacts can
// be large, so it copies rather than buffering the whole file.
func writeStream(in io.Reader, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
