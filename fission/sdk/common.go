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

package sdk

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
	uuid "github.com/satori/go.uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission"
	"github.com/fission/fission/controller/client"
	"github.com/fission/fission/crd"
	"github.com/fission/fission/fission/log"
	// "github.com/fission/fission/fission/log"
	"github.com/fission/fission/fission/portforward"
	storageSvcClient "github.com/fission/fission/storagesvc/client"
)

func GetFissionNamespace() string {
	fissionNamespace := os.Getenv("FISSION_NAMESPACE")
	return fissionNamespace
}

func GetKubeConfigPath() string {
	kubeConfig := os.Getenv("KUBECONFIG")
	if len(kubeConfig) == 0 {
		home := os.Getenv("HOME")
		kubeConfig = filepath.Join(home, ".kube", "config")

		if _, err := os.Stat(kubeConfig); os.IsNotExist(err) {
			log.Fatal("Couldn't find kubeconfig file. " +
				"Set the KUBECONFIG environment variable to your kubeconfig's path.")
		}
	}
	return kubeConfig
}

func GetServerUrl() string {
	return GetApplicationUrl("application=fission-api")
}

func GetApplicationUrl(selector string) string {
	var serverUrl string
	// Use FISSION_URL env variable if set; otherwise, port-forward to controller.
	fissionUrl := os.Getenv("FISSION_URL")
	if len(fissionUrl) == 0 {
		fissionNamespace := GetFissionNamespace()
		kubeConfig := GetKubeConfigPath()
		localPort := portforward.Setup(kubeConfig, fissionNamespace, selector)
		serverUrl = "http://127.0.0.1:" + localPort
	} else {
		serverUrl = fissionUrl
	}
	return serverUrl
}

func GetClient(serverUrl string) *client.Client {
	if len(serverUrl) == 0 {
		// starts local portforwarder etc.
		serverUrl = GetServerUrl()
	}

	isHTTPS := strings.Index(serverUrl, "https://") == 0
	isHTTP := strings.Index(serverUrl, "http://") == 0

	if !(isHTTP || isHTTPS) {
		serverUrl = "http://" + serverUrl
	}

	return client.MakeClient(serverUrl)
}

func MissingArgError(argName string) error {
	var message string
	if log.IsCliRun {
		message = fmt.Sprintf("Missing --%v argument", argName)
	} else {
		message = fmt.Sprintf("Missing argument %v", argName)
	}
	return errors.New(message)
}

func CheckErr(err error, action string) {
	if err != nil {
		log.Fatal(fmt.Sprintf("Failed to %v: %v", action, err))
	}
}

func CheckErrElseLogSuccess(err error, action string, successMsgFormat string, successMsgArgs ...interface{}) {
	if err != nil {
		log.Fatal(fmt.Sprintf("Failed to %v: %v", action, err))
	} else {
		log.Info(successMsgFormat, successMsgArgs)
	}
}

func FailedToError(err error, msg string) error {
	if err != nil {
		return fmt.Errorf("Failed to %v: %v", msg, err)
	}
	return nil
}

func HttpRequest(method, url, body string, headers []string) *http.Response {
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
	CheckErr(err, "create HTTP request")

	for _, header := range headers {
		headerKeyValue := strings.SplitN(header, ":", 2)
		if len(headerKeyValue) != 2 {
			CheckErr(errors.New(""), "create request without appropriate headers")
		}
		req.Header.Set(headerKeyValue[0], headerKeyValue[1])
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	CheckErr(err, "execute HTTP request")

	return resp
}

func FileSize(filePath string) (int64, error) {
	info, err := os.Stat(filePath)
	CheckErr(err, fmt.Sprintf("stat %v", filePath))
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func FileChecksum(fileName string) (*fission.Checksum, error) {
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
func CreateArchive(client *client.Client, fileName string, specFile string) (*fission.Archive, error) {
	var archive fission.Archive

	// fetch archive from arbitrary url if fileName is a url
	if strings.HasPrefix(fileName, "http://") || strings.HasPrefix(fileName, "https://") {
		fileName = DownloadToTempFile(fileName)
	}

	if len(specFile) > 0 {
		// create an ArchiveUploadSpec and reference it from the archive
		aus := &ArchiveUploadSpec{
			Name:         KubifyName(path.Base(fileName)),
			IncludeGlobs: []string{fileName},
		}
		// save the uploadspec
		err := SpecSave(*aus, specFile)
		if err != nil {
			return &fission.Archive{}, FailedToError(err, fmt.Sprintf("write spec file %v", specFile))
		}
		// create the archive
		ar := &fission.Archive{
			Type: fission.ArchiveTypeUrl,
			URL:  fmt.Sprintf("%v%v", ARCHIVE_URL_PREFIX, aus.Name),
		}
		return ar, nil
	}

	fileSize, err := FileSize(fileName)
	if err != nil {
		return nil, err
	}
	if fileSize < fission.ArchiveLiteralSizeLimit {
		contents := GetContents(fileName)
		archive.Type = fission.ArchiveTypeLiteral
		archive.Literal = contents
	} else {
		u := strings.TrimSuffix(client.Url, "/") + "/proxy/storage"
		ssClient := storageSvcClient.MakeClient(u)

		// TODO add a progress bar
		id, err := ssClient.Upload(fileName, nil)
		CheckErr(err, fmt.Sprintf("upload file %v", fileName))

		storageSvc, err := client.GetSvcURL("application=fission-storage")
		storageSvcURL := "http://" + storageSvc
		CheckErr(err, "get fission storage service name")

		// We make a new client with actual URL of Storage service so that the URL is not
		// pointing to 127.0.0.1 i.e. proxy. DON'T reuse previous ssClient
		pkgClient := storageSvcClient.MakeClient(storageSvcURL)
		archiveURL := pkgClient.GetUrl(id)

		archive.Type = fission.ArchiveTypeUrl
		archive.URL = archiveURL

		csum, err := FileChecksum(fileName)
		CheckErr(err, fmt.Sprintf("calculate checksum for file %v", fileName))

		archive.Checksum = *csum
	}
	return &archive, nil
}

func CreatePackage(client *client.Client, pkgNamespace, envName, envNamespace, srcArchiveName, deployArchiveName, buildcmd string, specFile string) (*metav1.ObjectMeta, error) {
	pkgSpec := fission.PackageSpec{
		Environment: fission.EnvironmentReference{
			Namespace: envNamespace,
			Name:      envName,
		},
	}
	var pkgStatus fission.BuildStatus = fission.BuildStatusSucceeded

	var pkgName string
	var err error
	var arch *fission.Archive
	if len(deployArchiveName) > 0 {
		if len(specFile) > 0 { // we should do this in all cases, i think
			pkgStatus = fission.BuildStatusNone
		}

		arch, err = CreateArchive(client, deployArchiveName, specFile)
		if err != nil {
			return nil, err
		}
		pkgSpec.Deployment = *arch
		pkgName = KubifyName(fmt.Sprintf("%v-%v", path.Base(deployArchiveName), uniuri.NewLen(4)))
	}
	if len(srcArchiveName) > 0 {
		arch, err = CreateArchive(client, srcArchiveName, specFile)
		if err != nil {
			return nil, err
		}
		pkgSpec.Source = *arch

		// set pending status to package
		pkgStatus = fission.BuildStatusPending
		pkgName = KubifyName(fmt.Sprintf("%v-%v", path.Base(srcArchiveName), uniuri.NewLen(4)))
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
		err := SpecSave(*pkg, specFile)
		CheckErr(err, "save package spec")
		return &pkg.Metadata, err
	} else {
		pkgMetadata, err := client.PackageCreate(pkg)
		CheckErr(err, "create package")
		return pkgMetadata, err
	}
}

func GetContents(filePath string) []byte {
	var code []byte
	var err error

	code, err = ioutil.ReadFile(filePath)
	CheckErr(err, fmt.Sprintf("read %v", filePath))
	return code
}

func GetTempDir() (string, error) {
	tmpDir := uuid.NewV4().String()
	tmpPath := filepath.Join(os.TempDir(), tmpDir)
	err := os.Mkdir(tmpPath, 0744)
	return tmpPath, err
}

func WriteArchiveToFile(fileName string, reader io.Reader) error {
	tmpDir, err := GetTempDir()
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
func DownloadToTempFile(fileUrl string) string {
	reader, err := DownloadURL(fileUrl)
	defer reader.Close()
	CheckErr(err, fmt.Sprintf("download from url: %v", fileUrl))

	tmpDir, err := GetTempDir()
	CheckErr(err, "create temp directory")

	tmpFilename := uuid.NewV4().String()
	destination := filepath.Join(tmpDir, tmpFilename)
	err = os.Mkdir(tmpDir, 0744)
	CheckErr(err, "create temp directory")

	err = WriteArchiveToFile(destination, reader)
	CheckErr(err, "write archive to file")

	return destination
}

// downloadURL downloads file from given url
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

// make a kubernetes compliant name out of an arbitrary string
func KubifyName(old string) string {
	// Kubernetes maximum name length (for some names; others can be 253 chars)
	maxLen := 63

	newName := strings.ToLower(old)

	// replace disallowed chars with '-'
	inv, err := regexp.Compile("[^-a-z0-9]")
	CheckErr(err, "compile regexp")
	newName = string(inv.ReplaceAll([]byte(newName), []byte("-")))

	// trim leading non-alphabetic
	leadingnonalpha, err := regexp.Compile("^[^a-z]+")
	CheckErr(err, "compile regexp")
	newName = string(leadingnonalpha.ReplaceAll([]byte(newName), []byte{}))

	// trim trailing
	trailing, err := regexp.Compile("[^a-z0-9]+$")
	CheckErr(err, "compile regexp")
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

func GetFissionAPIVersion(apiUrl string) (string, error) {
	resp, err := http.Get(apiUrl)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(body), "\n"), nil
}

// returns one of http.Method*
func GetMethod(method string) string {
	switch strings.ToUpper(method) {
	case "GET":
		return http.MethodGet
	case "HEAD":
		return http.MethodHead
	case "POST":
		return http.MethodPost
	case "PUT":
		return http.MethodPut
	case "PATCH":
		return http.MethodPatch
	case "DELETE":
		return http.MethodDelete
	case "CONNECT":
		return http.MethodConnect
	case "OPTIONS":
		return http.MethodOptions
	case "TRACE":
		return http.MethodTrace
	}
	log.Fatal(fmt.Sprintf("Invalid HTTP Method %v", method))
	return ""
}

func CheckFunctionExistence(fissionClient *client.Client, fnName string, fnNamespace string) {
	meta := &metav1.ObjectMeta{
		Name:      fnName,
		Namespace: fnNamespace,
	}

	_, err := fissionClient.FunctionGet(meta)
	if err != nil {
		fmt.Printf("function '%v' does not exist, use 'fission function create --name %v ...' to create the function\n", fnName, fnName)
	}
}
