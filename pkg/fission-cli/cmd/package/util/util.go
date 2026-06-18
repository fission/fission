// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/util"
	storageSvcClient "github.com/fission/fission/pkg/storagesvc/client"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/uuid"
)

// UploadArchiveFile uploads fileName to storagesvc and returns the resulting
// Archive. When namespace is non-empty the upload is scoped to that tenant, so
// the archive id carries the namespace and storagesvc isolates it from other
// tenants; an empty namespace uploads master-derived/unscoped (the legacy form,
// for callers without an unambiguous owning namespace such as spec apply).
func UploadArchiveFile(ctx context.Context, client cmd.Client, fileName, namespace string) (*fv1.Archive, error) {
	var archive fv1.Archive

	size, err := utils.FileSize(fileName)
	if err != nil {
		return nil, err
	}

	if size < fv1.ArchiveLiteralSizeLimit {
		archive.Type = fv1.ArchiveTypeLiteral
		archive.Literal, err = GetContents(fileName)
		if err != nil {
			return nil, err
		}
	} else {
		storagesvcURL, err := util.GetStorageURL(ctx, client)
		if err != nil {
			return nil, fmt.Errorf("error getting fission storage service URL: %w", err)
		}

		hmacSecret, err := storageSvcClient.HMACSecretFromCluster(ctx, client.KubernetesClient, util.GetFissionNamespace())
		if err != nil {
			return nil, fmt.Errorf("error reading internal-auth secret: %w", err)
		}

		// Scope the upload to the owning namespace when known, so the archive id
		// is namespace-tagged and storagesvc isolates it; falls back to
		// master-derived/unscoped for an empty namespace.
		storageClient := storageSvcClient.MakeClientNS(storagesvcURL.String(), hmacSecret, namespace)
		// TODO add a progress bar
		id, err := storageClient.Upload(ctx, fileName, nil)
		if err != nil {
			return nil, fmt.Errorf("error uploading to fission storage service: %w", err)
		}

		archiveURL, err := getArchiveURL(ctx, client, id, storagesvcURL)
		if err != nil {
			return nil, fmt.Errorf("could not get URL of archive: %w", err)
		}

		archive.Type = fv1.ArchiveTypeUrl
		archive.URL = archiveURL

		csum, err := utils.GetFileChecksum(fileName)
		if err != nil {
			return nil, fmt.Errorf("calculate checksum for file %v: %w", fileName, err)
		}

		archive.Checksum = *csum
	}

	return &archive, nil
}

func getArchiveURL(ctx context.Context, client cmd.Client, archiveID string, serverURL *url.URL) (archiveURL string, err error) {
	hmacSecret, err := storageSvcClient.HMACSecretFromCluster(ctx, client.KubernetesClient, util.GetFissionNamespace())
	if err != nil {
		return "", err
	}
	infoClient := storageSvcClient.MakeClient(serverURL.String(), hmacSecret)
	resp, err := infoClient.Info(ctx, archiveID)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("error getting URL. Exited with Status:  %s", resp.Status)
	}

	storageType := resp.Header.Get("X-FISSION-STORAGETYPE")

	switch storageType {
	case "local":
		storageSvc, err := util.GetSvcName(ctx, client.KubernetesClient, "fission-storage")
		if err != nil {
			return "", err
		}
		storagesvcURL := "http://" + storageSvc
		// GetUrl is a string formatter; HMAC secret not needed here.
		ssClient := storageSvcClient.MakeClient(storagesvcURL, nil)
		return ssClient.GetUrl(archiveID), nil
	case "s3":
		storageBucket := resp.Header.Get("X-FISSION-BUCKET")
		s3url := fmt.Sprintf("https://%s.s3.amazonaws.com/%s", storageBucket, archiveID)
		return s3url, nil
	}
	return "", nil
}
func GetContents(filePath string) ([]byte, error) {
	code, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("error reading %v: %w", filePath, err)
	}
	return code, nil
}

// DownloadToTempFile fetches archive file from arbitrary url
// and write it to temp file for further usage
func DownloadToTempFile(fileUrl string) (string, error) {
	reader, err := DownloadURL(fileUrl)
	if err != nil {
		return "", fmt.Errorf("error downloading from url: %v: %w", fileUrl, err)
	}
	defer reader.Close()

	tmpDir, err := utils.GetTempDir()
	if err != nil {
		return "", fmt.Errorf("error creating temp directory %v: %w", tmpDir, err)
	}

	tmpFilename := uuid.NewString()
	destination := filepath.Join(tmpDir, tmpFilename)

	err = WriteArchiveToFile(destination, reader)
	if err != nil {
		return "", fmt.Errorf("error writing archive to file %v: %w", destination, err)
	}

	return destination, nil
}

// DownloadURL downloads file from given url
func DownloadURL(fileUrl string) (io.ReadCloser, error) {
	resp, err := http.Get(fileUrl)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%v - HTTP response returned non 200 status", resp.StatusCode)
	}
	return resp.Body, nil
}

func DownloadStrorageURL(ctx context.Context, client cmd.Client, fileUrl string) (io.ReadCloser, error) {
	var resp *http.Response
	var err error

	valid, err := validArchiveURL(fileUrl)
	if err != nil {
		return nil, err
	}

	if valid {
		storagesvcURL, err := util.GetStorageURL(ctx, client)
		if err != nil {
			return nil, err
		}

		url, err := url.Parse(fileUrl)
		if err != nil {
			return nil, err
		}
		id := url.Query().Get("id")

		hmacSecret, err := storageSvcClient.HMACSecretFromCluster(ctx, client.KubernetesClient, util.GetFissionNamespace())
		if err != nil {
			return nil, err
		}

		ssClient := storageSvcClient.MakeClient(storagesvcURL.String(), hmacSecret)
		resp, err = ssClient.GetFile(ctx, id)
		if err != nil {
			return nil, err
		}
	} else {
		resp, err = http.Get(fileUrl)
	}

	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%v - HTTP response returned non 200 status", resp.StatusCode)
	}
	return resp.Body, nil
}

func WriteArchiveToFile(fileName string, reader io.Reader) error {
	// Create the target file directly
	file, err := os.Create(fileName)
	if err != nil {
		return err
	}
	defer file.Close()

	// Copy data from the reader to the target file
	_, err = io.Copy(file, reader)
	if err != nil {
		return err
	}

	// Function archives may contain proprietary source; restrict to the
	// owner. The CLI runs as the invoking user, so this is the right
	// scope — collaborators get archives via re-running `fission package
	// fetch`, not by reading each other's home directories.
	err = os.Chmod(fileName, 0o600)
	if err != nil {
		return err
	}

	return nil
}

// PrintPackageSummary prints package information, conditions and build logs.
func PrintPackageSummary(writer io.Writer, pkg *fv1.Package) {
	// replace escaped line breaker character
	buildlog := strings.ReplaceAll(pkg.Status.BuildLog, `\n`, "\n")
	w := util.NewTabWriter(writer)
	fmt.Fprintf(w, "%v\t%v\n", "Name:", pkg.Name)
	fmt.Fprintf(w, "%v\t%v\n", "Environment:", pkg.Spec.Environment.Name)
	fmt.Fprintf(w, "%v\t%v\n", "Status:", pkg.Status.BuildStatus)
	fmt.Fprintf(w, "%v\t%v\n", "Deployment:", DescribeDeploymentArchive(&pkg.Spec.Deployment))
	w.Flush()
	util.PrintConditionsTo(writer, pkg.Status.Conditions)
	fmt.Fprintf(writer, "%v\n%v", "Build Logs:", buildlog)
}

// DescribeDeploymentArchive renders a one-line, user-facing description of
// how a package's code is stored and therefore delivered to function pods.
func DescribeDeploymentArchive(a *fv1.Archive) string {
	switch {
	case a.OCI != nil:
		ref := a.OCI.Image
		if a.OCI.Digest != "" {
			short := strings.TrimPrefix(a.OCI.Digest, "sha256:")
			if len(short) > 12 {
				short = short[:12]
			}
			ref += " (digest " + short + ")"
		}
		return "OCI image " + ref + " — mounted directly on clusters with image volumes, pulled by the fetcher otherwise"
	case len(a.Literal) > 0:
		return fmt.Sprintf("embedded in the package (%d bytes)", len(a.Literal))
	case a.URL != "":
		return "archive in storage (" + a.URL + ")"
	default:
		return "none"
	}
}

// validArchiveURL checks if the given URL is a valid archive URL
func validArchiveURL(urlStr string) (bool, error) {
	// Parse the URL string into a URL object
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return false, fmt.Errorf("failed to parse URL: %v", err)
	}

	// Check if the path starts with /v1/archive
	if !strings.HasPrefix(parsedURL.Path, "/v1/archive") {
		return false, nil
	}

	// Get query parameters
	queryParams := parsedURL.Query()

	// Check if 'id' parameter exists
	if queryParams.Get("id") == "" {
		return false, nil
	}

	// URL matches all criteria
	return true, nil
}
