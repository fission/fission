/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package builder

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

const (
	// OCIBuildEndpoint is the route the buildermgr posts to when a Package
	// targets a buildkit-flavored Environment. Mounted alongside the legacy
	// "/" handler so the same builder pod can serve both paths.
	OCIBuildEndpoint = "/buildkit"

	// envBuildKitContextDir contains the path of the unpacked source
	// directory passed to buildctl.
	envBuildKitContextDir = "BUILDKIT_CONTEXT"

	// envBuildKitImageRef is the fully qualified destination image reference
	// the build pushes to.
	envBuildKitImageRef = "BUILDKIT_IMAGE_REF"

	// envBuildKitBaseImage is the base layer the produced OCI artifact is
	// rooted at, defaulting to the environment's runtime image.
	envBuildKitBaseImage = "BUILDKIT_BASE_IMAGE"
)

type (
	// OCIBuildRequest is the payload buildermgr POSTs to the BuildKit-aware
	// builder pod. The builder is expected to: fetch source from the shared
	// volume, run buildctl producing an image rooted at BaseImage, push it
	// to ImageRef in the registry described by RegistryURL, and respond
	// with the resulting digest plus logs.
	OCIBuildRequest struct {
		// SrcPkgFilename names the source archive on the shared volume,
		// matching the filename returned by the fetcher.
		SrcPkgFilename string `json:"srcPkgFilename"`

		// ImageRef is the fully qualified destination image reference,
		// e.g. "ghcr.io/myorg/fission-fns/hello-pkg:abc123".
		ImageRef string `json:"imageRef"`

		// BaseImage is the runtime image the build is layered on top of.
		BaseImage string `json:"baseImage"`

		// RegistryURL is the registry endpoint (without scheme) the image
		// is pushed to. Used by the builder to authenticate.
		RegistryURL string `json:"registryUrl,omitempty"`

		// (Optional) BuildArgs are forwarded to buildctl as --opt entries.
		BuildArgs map[string]string `json:"buildArgs,omitempty"`
	}

	// OCIBuildResponse is the body the BuildKit builder returns on a
	// completed build. ImageRef is normally identical to the request's
	// ImageRef but is echoed back to make the contract symmetric. Digest
	// is the sha256 content hash of the produced manifest, suitable for
	// pinning the Function's deployment.
	OCIBuildResponse struct {
		ImageRef  string `json:"imageRef"`
		Digest    string `json:"digest"`
		BuildLogs string `json:"buildLogs"`
	}
)

// OCIHandler is the HTTP handler the builder pod exposes for BuildKit
// builds. The pure-Go skeleton here calls out to a `buildkit-build` binary
// the BuildKit-flavored builder image is expected to provide; the actual
// buildctl invocation lives in that binary so that the builder image can
// be swapped without recompiling fission-bundle.
//
// The skeleton serves three purposes:
//
//  1. Lock the wire contract between buildermgr and the builder image.
//  2. Provide a smoke-testable endpoint that returns a structured error
//     when no buildkit binary is installed (so users get a clear message
//     rather than a 404).
//  3. Surface logs and digest in the OCIBuildResponse so the buildermgr
//     can populate Package.Status uniformly for both builder kinds.
func (builder *Builder) OCIHandler(w http.ResponseWriter, r *http.Request) {
	logger := otelUtils.LoggerWithTraceID(r.Context(), builder.logger)

	if r.Method != http.MethodPost {
		builder.replyOCI(r.Context(), w, "", "", fmt.Sprintf("method not allowed: %s", r.Method), http.StatusMethodNotAllowed)
		return
	}
	startTime := time.Now()
	defer func() {
		logger.Info("oci build request complete", "elapsed_time", time.Since(startTime))
	}()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		builder.replyOCI(r.Context(), w, "", "", fmt.Sprintf("error reading body: %s", err), http.StatusInternalServerError)
		return
	}
	var req OCIBuildRequest
	if err := json.Unmarshal(body, &req); err != nil {
		builder.replyOCI(r.Context(), w, "", "", fmt.Sprintf("error parsing body: %s", err), http.StatusBadRequest)
		return
	}
	if req.ImageRef == "" || req.SrcPkgFilename == "" {
		builder.replyOCI(r.Context(), w, "", "", "imageRef and srcPkgFilename are required", http.StatusBadRequest)
		return
	}

	srcPath := filepath.Join(builder.sharedVolumePath, req.SrcPkgFilename)
	if _, err := os.Stat(srcPath); err != nil {
		builder.replyOCI(r.Context(), w, "", "", fmt.Sprintf("source archive not found: %s", err), http.StatusBadRequest)
		return
	}

	cmd := exec.CommandContext(r.Context(), "buildkit-build")
	cmd.Env = append(os.Environ(),
		envBuildKitContextDir+"="+srcPath,
		envBuildKitImageRef+"="+req.ImageRef,
		envBuildKitBaseImage+"="+req.BaseImage,
	)
	for k, v := range req.BuildArgs {
		cmd.Env = append(cmd.Env, fmt.Sprintf("BUILDKIT_OPT_%s=%s", k, v))
	}
	out, runErr := cmd.CombinedOutput()
	logs := string(out)
	if runErr != nil {
		// PathError indicates the buildkit-build binary is not present in
		// this image; surface that as 501 so users distinguish "wrong
		// builder image" from "build failed".
		if _, ok := runErr.(*exec.Error); ok {
			builder.replyOCI(r.Context(), w, "", logs,
				"buildkit-build binary not found in builder image; install a BuildKit-enabled builder image to use Builder.Kind=buildkit",
				http.StatusNotImplemented)
			return
		}
		builder.replyOCI(r.Context(), w, req.ImageRef, logs, fmt.Sprintf("buildkit-build failed: %s", runErr), http.StatusInternalServerError)
		return
	}

	digest := parseDigestFromLogs(logs)
	resp := OCIBuildResponse{
		ImageRef:  req.ImageRef,
		Digest:    digest,
		BuildLogs: logs,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	if err := enc.Encode(resp); err != nil {
		logger.Error(err, "error writing oci build response")
	}
}

// parseDigestFromLogs scans buildkit-build output for the manifest digest
// line. The contract: the build script must emit a line starting with
// "manifest-digest:" followed by the sha256 reference. Empty result means
// the digest could not be determined; buildermgr leaves it unset rather
// than failing the build, since the image may still be usable by tag.
func parseDigestFromLogs(logs string) string {
	const marker = "manifest-digest:"
	for _, line := range strings.Split(logs, "\n") {
		if i := strings.Index(line, marker); i >= 0 {
			if candidate := strings.TrimSpace(line[i+len(marker):]); candidate != "" {
				return candidate
			}
		}
	}
	return ""
}

func (builder *Builder) replyOCI(_ /*ctx*/ any, w http.ResponseWriter, imageRef, logs, errMsg string, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	resp := struct {
		ImageRef  string `json:"imageRef,omitempty"`
		Digest    string `json:"digest,omitempty"`
		BuildLogs string `json:"buildLogs,omitempty"`
		Error     string `json:"error,omitempty"`
	}{ImageRef: imageRef, BuildLogs: logs, Error: errMsg}
	_ = json.NewEncoder(w).Encode(resp)
}
