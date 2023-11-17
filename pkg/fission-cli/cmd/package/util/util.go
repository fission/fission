/*
Copyright 2019 The Fission Authors.

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
	"text/tabwriter"

	"github.com/pkg/errors"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/util"
	storageSvcClient "github.com/fission/fission/pkg/storagesvc/client"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/uuid"
)

func UploadArchiveFile(ctx context.Context, client cmd.Client, fileName string) (*fv1.Archive, error) {
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
			return nil, errors.Wrapf(err, "error getting fission storage service URL")
		}

		storageClient := storageSvcClient.MakeClient(storagesvcURL.String())
		// TODO add a progress bar
		id, err := storageClient.Upload(ctx, fileName, nil)
		if err != nil {
			return nil, errors.Wrapf(err, "error uploading to fission storage service")
		}

		archiveURL, err := getArchiveURL(ctx, client, id, storagesvcURL)
		if err != nil {
			return nil, errors.Wrapf(err, "could not get URL of archive")
		}

		archive.Type = fv1.ArchiveTypeUrl
		archive.URL = archiveURL

		csum, err := utils.GetFileChecksum(fileName)
		if err != nil {
			return nil, errors.Wrapf(err, "calculate checksum for file %v", fileName)
		}

		archive.Checksum = *csum
	}

	return &archive, nil
}

func getArchiveURL(ctx context.Context, client cmd.Client, archiveID string, serverURL *url.URL) (archiveURL string, err error) {
	relativeURL, _ := url.Parse(util.FISSION_STORAGE_URI)

	queryString := relativeURL.Query()
	queryString.Set("id", archiveID)
	relativeURL.RawQuery = queryString.Encode()

	storageAccessURL := serverURL.ResolveReference(relativeURL)

	resp, err := http.Head(storageAccessURL.String())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("error getting URL. Exited with Status:  %s", resp.Status)
	}

	storageType := resp.Header.Get("X-FISSION-STORAGETYPE")

	if storageType == "local" {
		storagesvcURL, err := util.GetStorageURL(ctx, client)
		if err != nil {
			return "", err
		}
		client := storageSvcClient.MakeClient(storagesvcURL.String())
		return client.GetUrl(archiveID), nil
	} else if storageType == "s3" {
		storageBucket := resp.Header.Get("X-FISSION-BUCKET")
		s3url := fmt.Sprintf("https://%s.s3.amazonaws.com/%s", storageBucket, archiveID)
		return s3url, nil
	}
	return "", nil
}
func GetContents(filePath string) ([]byte, error) {
	code, err := os.ReadFile(filePath)
	if err != nil {
		return nil, errors.Wrapf(err, "error reading %v", filePath)
	}
	return code, nil
}

// DownloadToTempFile fetches archive file from arbitrary url
// and write it to temp file for further usage
func DownloadToTempFile(fileUrl string) (string, error) {
	reader, err := DownloadURL(fileUrl)
	if err != nil {
		return "", errors.Wrapf(err, "error downloading from url: %v", fileUrl)
	}
	defer reader.Close()

	tmpDir, err := utils.GetTempDir()
	if err != nil {
		return "", errors.Wrapf(err, "error creating temp directory %v", tmpDir)
	}

	tmpFilename := uuid.NewString()
	destination := filepath.Join(tmpDir, tmpFilename)

	err = WriteArchiveToFile(destination, reader)
	if err != nil {
		return "", errors.Wrapf(err, "error writing archive to file %v", destination)
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
	storagesvcURL, err := util.GetStorageURL(ctx, client)
	if err != nil {
		return nil, err
	}

	if strings.HasPrefix(fileUrl, storagesvcURL.String()+"/v1/archive?id=") {
		url, err := url.Parse(fileUrl)
		if err != nil {
			return nil, err
		}
		id := url.Query().Get("id")

		client := storageSvcClient.MakeClient(storagesvcURL.String())
		resp, err = client.GetFile(ctx, id)
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
	tmpDir, err := utils.GetTempDir()
	if err != nil {
		return err
	}

	tmpFileName := uuid.NewString()

	path := filepath.Join(tmpDir, tmpFileName+".tmp")
	w, err := os.Create(path)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, reader)
	if err != nil {
		return err
	}
	err = os.Chmod(path, 0644)
	if err != nil {
		return err
	}

	err = os.Rename(path, fileName)
	if err != nil {
		return err
	}

	return nil
}

// PrintPackageSummary prints package information and build logs.
func PrintPackageSummary(writer io.Writer, pkg *fv1.Package) {
	// replace escaped line breaker character
	buildlog := strings.ReplaceAll(pkg.Status.BuildLog, `\n`, "\n")
	w := tabwriter.NewWriter(writer, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\n", "Name:", pkg.ObjectMeta.Name)
	fmt.Fprintf(w, "%v\t%v\n", "Environment:", pkg.Spec.Environment.Name)
	fmt.Fprintf(w, "%v\t%v\n", "Status:", pkg.Status.BuildStatus)
	fmt.Fprintf(w, "%v\n%v", "Build Logs:", buildlog)
	w.Flush()
}
