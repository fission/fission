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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fission/fission/pkg/utils"
)

// The builder contract (pkg/builder, cmd/builder): the env builder image runs a
// server on 8001 with the shared volume at builderSharedPath; POST / a
// {srcPkgFilename, command} and it writes the compiled artifact under the shared
// volume and returns its filename.
const (
	builderSharedPath = "/packages"
	builderPort       = 8001
	builderSrcDirName = "src"
	builderTimeout    = 30 * time.Second
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
	fmt.Fprintf(stderr, "Building with %s ...\n", cfg.builderImage)
	id, err := rt.StartContainer(ctx, containerSpec{
		Image:  cfg.builderImage,
		Mounts: []bindMount{{HostDir: sharedDir, ContainerDir: builderSharedPath}},
		Ports:  []portMapping{{Host: hostPort, Container: builderPort}},
	})
	if err != nil {
		return err
	}
	defer stopQuietly(ctx, rt, id)

	if err := waitForServer(ctx, hostPort); err != nil {
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

	if err := copyTree(filepath.Join(sharedDir, resp.ArtifactFilename), dstDeploy); err != nil {
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
	url := fmt.Sprintf("http://127.0.0.1:%d/", hostPort)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return buildResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: builderTimeout}
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

// copyTree copies src to dst, where src may be a single file or a directory tree.
func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode())
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, fi.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	// Stream the copy: build artifacts (compiled binaries) can be large.
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
