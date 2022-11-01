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
	uuid "github.com/satori/go.uuid"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	storageSvcClient "github.com/fission/fission/pkg/storagesvc/client"
	"github.com/fission/fission/pkg/utils"
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
		u := strings.TrimSuffix(client.DefaultClientset.ServerURL(), "/") + "/proxy/storage"
		ssClient := storageSvcClient.MakeClient(u)

		// TODO add a progress bar
		id, err := ssClient.Upload(ctx, fileName, nil)
		if err != nil {
			return nil, errors.Wrapf(err, "error uploading file %v", fileName)
		}

		storageSvc, err := client.DefaultClientset.V1().Misc().GetSvcURL("application=fission-storage")
		storageSvcURL := "http://" + storageSvc
		if err != nil {
			return nil, errors.Wrapf(err, "error getting fission storage service name")
		}

		// We make a new client with actual URL of Storage service so that the URL is not
		// pointing to 127.0.0.1 i.e. proxy. DON'T reuse previous ssClient
		pkgClient := storageSvcClient.MakeClient(storageSvcURL)
		archiveURL := pkgClient.GetUrl(id)

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

	id, err := uuid.NewV4()
	if err != nil {
		return "", errors.Wrapf(err, "error generating UUID")
	}
	tmpFilename := id.String()
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

func WriteArchiveToFile(fileName string, reader io.Reader) error {
	tmpDir, err := utils.GetTempDir()
	if err != nil {
		return err
	}
	id, err := uuid.NewV4()
	if err != nil {
		return err
	}
	tmpFileName := id.String()

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

// DownloadStoragesvcURL downloads and return archive content with given storage service url
func DownloadStoragesvcURL(client client.Interface, fileUrl string) (io.ReadCloser, error) {
	u, err := url.ParseRequestURI(fileUrl)
	if err != nil {
		return nil, err
	}

	// replace in-cluster storage service host with controller server url
	fileDownloadUrl := strings.TrimSuffix(client.ServerURL(), "/") + "/proxy/storage/" + u.RequestURI()
	reader, err := DownloadURL(fileDownloadUrl)
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("error downloading from storage service url: %v", fileUrl))
	}

	return reader, nil
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
