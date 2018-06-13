/*
Copyright 2016 The Fission Authors.

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

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dchest/uniuri"
	"github.com/fission/fission/fission/log"
	uuid "github.com/satori/go.uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission"
	"github.com/fission/fission/controller/client"
	"github.com/fission/fission/crd"
	storageSvcClient "github.com/fission/fission/storagesvc/client"
)

func getClient(serverUrl string) *client.Client {
	if len(serverUrl) == 0 {
		// starts local portforwarder etc.
		serverUrl = getServerUrl()
	}

	isHTTPS := strings.Index(serverUrl, "https://") == 0
	isHTTP := strings.Index(serverUrl, "http://") == 0

	if !(isHTTP || isHTTPS) {
		serverUrl = "http://" + serverUrl
	}

	return client.MakeClient(serverUrl)
}

func checkErr(err error, msg string) {
	if err != nil {
		log.Fatal(fmt.Sprintf("Failed to %v: %v", msg, err))
	}
}

func httpRequest(method, url, body string, headers []string) *http.Response {
	if method == "" {
		method = "GET"
	}

	if method != http.MethodGet &&
		method != http.MethodDelete &&
		method != http.MethodPost &&
		method != http.MethodPut {
		log.Fatal(fmt.Sprintf("Invalid HTTP method '%s'.", method))
	}

	req, err := http.NewRequest(method, url, strings.NewReader(body))
	checkErr(err, "create HTTP request")

	for _, header := range headers {
		headerKeyValue := strings.SplitN(header, ":", 2)
		if len(headerKeyValue) != 2 {
			checkErr(errors.New(""), "create request without appropriate headers")
		}
		req.Header.Set(headerKeyValue[0], headerKeyValue[1])
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	checkErr(err, "execute HTTP request")

	return resp
}

func fileSize(filePath string) int64 {
	info, err := os.Stat(filePath)
	checkErr(err, fmt.Sprintf("stat %v", filePath))
	return info.Size()
}

func fileChecksum(fileName string) (*fission.Checksum, error) {
	f, err := os.Open(fileName)
	if err != nil {
		return nil, fmt.Errorf("failed to open file %v: %v", fileName, err)
	}
	defer f.Close()

	h := sha256.New()
	_, err = io.Copy(h, f)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate checksum for %v", fileName)
	}

	return &fission.Checksum{
		Type: fission.ChecksumTypeSHA256,
		Sum:  hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// upload a file and return a fission.Archive
func createArchive(client *client.Client, fileName string, specFile string) *fission.Archive {
	var archive fission.Archive

	// fetch archive from arbitrary url if fileName is a url
	if strings.HasPrefix(fileName, "http://") || strings.HasPrefix(fileName, "https://") {
		fileName = downloadToTempFile(fileName)
	}

	if len(specFile) > 0 {
		// create an ArchiveUploadSpec and reference it from the archive
		aus := &ArchiveUploadSpec{
			Name:         kubifyName(path.Base(fileName)),
			IncludeGlobs: []string{fileName},
		}
		// save the uploadspec
		err := specSave(*aus, specFile)
		checkErr(err, fmt.Sprintf("write spec file %v", specFile))
		// create the archive
		ar := &fission.Archive{
			Type: fission.ArchiveTypeUrl,
			URL:  fmt.Sprintf("%v%v", ARCHIVE_URL_PREFIX, aus.Name),
		}
		return ar
	}

	if fileSize(fileName) < fission.ArchiveLiteralSizeLimit {
		contents := getContents(fileName)
		archive.Type = fission.ArchiveTypeLiteral
		archive.Literal = contents
	} else {
		// make a kubernetes client
		_, kubeClient, _, err := crd.GetKubernetesClient()
		if err != nil {
			log.Fatal(err.Error())
		}

		fissionNamespace := os.Getenv("FISSION_NAMESPACE")

		// get svc end point for storagesvc
		service, err := kubeClient.CoreV1().Services(fissionNamespace).Get("storagesvc", metav1.GetOptions{})
		if err != nil {
			log.Fatal(fmt.Sprintf("Error getting storage service object from kubernetes :%v", err.Error()))
		}

		u := strings.TrimSuffix(client.Url, "/") + "/proxy/storage"
		ssClient := storageSvcClient.MakeClient(u)

		// TODO add a progress bar
		id, err := ssClient.Upload(fileName, nil)
		checkErr(err, fmt.Sprintf("upload file %v", fileName))

		// this needs to be storagesvc.fission
		storageSvcEndpoint := fmt.Sprintf("http://%s.%s/", service.Name, service.Namespace)
		storageServiceClient := storageSvcClient.MakeClient(storageSvcEndpoint)
		archiveUrl := storageServiceClient.GetUrl(id)

		archive.Type = fission.ArchiveTypeUrl
		archive.URL = archiveUrl

		csum, err := fileChecksum(fileName)
		checkErr(err, fmt.Sprintf("calculate checksum for file %v", fileName))

		archive.Checksum = *csum
	}
	return &archive
}

func createPackage(client *client.Client, pkgNamespace, envName, envNamespace, srcArchiveName, deployArchiveName, buildcmd string, specFile string) *metav1.ObjectMeta {
	pkgSpec := fission.PackageSpec{
		Environment: fission.EnvironmentReference{
			Namespace: envNamespace,
			Name:      envName,
		},
	}
	var pkgStatus fission.BuildStatus = fission.BuildStatusSucceeded

	var pkgName string
	if len(deployArchiveName) > 0 {
		if len(specFile) > 0 { // we should do this in all cases, i think
			pkgStatus = fission.BuildStatusNone
		}
		pkgSpec.Deployment = *createArchive(client, deployArchiveName, specFile)
		pkgName = kubifyName(fmt.Sprintf("%v-%v", path.Base(deployArchiveName), uniuri.NewLen(4)))
	}
	if len(srcArchiveName) > 0 {
		pkgSpec.Source = *createArchive(client, srcArchiveName, specFile)
		// set pending status to package
		pkgStatus = fission.BuildStatusPending
		pkgName = kubifyName(fmt.Sprintf("%v-%v", path.Base(srcArchiveName), uniuri.NewLen(4)))
	}

	if len(buildcmd) > 0 {
		pkgSpec.BuildCommand = buildcmd
	}

	if len(pkgName) == 0 {
		pkgName = strings.ToLower(uuid.NewV4().String())
	}
	pkg := &crd.Package{
		Metadata: metav1.ObjectMeta{
			Name:      pkgName,
			Namespace: pkgNamespace,
		},
		Spec: pkgSpec,
		Status: fission.PackageStatus{
			BuildStatus: pkgStatus,
		},
	}

	if len(specFile) > 0 {
		err := specSave(*pkg, specFile)
		checkErr(err, "save package spec")
		return &pkg.Metadata
	} else {
		pkgMetadata, err := client.PackageCreate(pkg)
		checkErr(err, "create package")
		return pkgMetadata
	}
}

func getContents(filePath string) []byte {
	var code []byte
	var err error

	code, err = ioutil.ReadFile(filePath)
	checkErr(err, fmt.Sprintf("read %v", filePath))
	return code
}

func getTempDir() (string, error) {
	tmpDir := uuid.NewV4().String()
	tmpPath := filepath.Join(os.TempDir(), tmpDir)
	err := os.Mkdir(tmpPath, 0744)
	return tmpPath, err
}

func writeArchiveToFile(fileName string, reader io.Reader) error {
	tmpDir, err := getTempDir()
	if err != nil {
		return err
	}

	path := filepath.Join(tmpDir, fileName+".tmp")
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

// downloadToTempFile fetches archive file from arbitrary url
// and write it to temp file for further usage
func downloadToTempFile(fileUrl string) string {
	reader, err := downloadURL(fileUrl)
	defer reader.Close()
	checkErr(err, fmt.Sprintf("download from url: %v", fileUrl))

	tmpDir, err := getTempDir()
	checkErr(err, "create temp directory")

	tmpFilename := uuid.NewV4().String()
	destination := filepath.Join(tmpDir, tmpFilename)
	err = os.Mkdir(tmpDir, 0744)
	checkErr(err, "create temp directory")

	err = writeArchiveToFile(destination, reader)
	checkErr(err, "write archive to file")

	return destination
}

// downloadURL downloads file from given url
func downloadURL(fileUrl string) (io.ReadCloser, error) {
	resp, err := http.Get(fileUrl)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%v - HTTP response returned non 200 status", resp.StatusCode)
	}
	return resp.Body, nil
}

// make a kubernetes compliant name out of an arbitrary string
func kubifyName(old string) string {
	// Kubernetes maximum name length (for some names; others can be 253 chars)
	maxLen := 63

	newName := strings.ToLower(old)

	// replace disallowed chars with '-'
	inv, err := regexp.Compile("[^-a-z0-9]")
	checkErr(err, "compile regexp")
	newName = string(inv.ReplaceAll([]byte(newName), []byte("-")))

	// trim leading non-alphabetic
	leadingnonalpha, err := regexp.Compile("^[^a-z]+")
	checkErr(err, "compile regexp")
	newName = string(leadingnonalpha.ReplaceAll([]byte(newName), []byte{}))

	// trim trailing
	trailing, err := regexp.Compile("[^a-z0-9]+$")
	checkErr(err, "compile regexp")
	newName = string(trailing.ReplaceAll([]byte(newName), []byte{}))

	// truncate to length
	if len(newName) > maxLen {
		newName = newName[0:maxLen]
	}

	// if we removed everything, call this thing "default". maybe
	// we should generate a unique name...
	if len(newName) == 0 {
		newName = "default"
	}

	return newName
}
