package builder

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/mholt/archiver"

	"github.com/fission/fission"
)

const (
	pathPrefix      = "/tmp/fission"
	srcPkgPath      = pathPrefix + "/srcPkg"
	deployPkgPath   = pathPrefix + "/deployPkg"
	archiveFilename = "archive.zip"

	// supported environment variables
	envSrcPkg    = "SRC_PKG"
	envDeployPkg = "DEPLOY_PKG"
)

type (
	Builder struct {
		sharedVolumePath string
	}
)

func MakeBuilder(sharedVolumePath string) *Builder {
	return &Builder{
		sharedVolumePath: sharedVolumePath,
	}
}

func (builder *Builder) Handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "", 404)
		return
	}

	startTime := time.Now()
	defer func() {
		elapsed := time.Now().Sub(startTime)
		log.Printf("elapsed time in build request = %v", elapsed)
	}()

	// parse request
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Printf("Error reading request body")
		http.Error(w, err.Error(), 500)
		return
	}
	var req fission.PackageBuildRequest
	err = json.Unmarshal(body, &req)
	if err != nil {
		log.Printf("Error reading request body: %v", err)
		http.Error(w, err.Error(), 500)
		return
	}
	log.Printf("builder received request: %v", req)

	srcPkgArchivePath := filepath.Join(builder.sharedVolumePath, req.SrcPkgFilename)
	err = builder.unarchive(srcPkgArchivePath, srcPkgPath)
	if err != nil {
		e := errors.New(fmt.Sprintf("Failed to unarchive source package: %v", err))
		http.Error(w, e.Error(), 500)
		return
	}

	buildLogs, err := builder.build(req.BuildCommand, srcPkgPath)
	if err != nil {
		e := errors.New(fmt.Sprintf("Failed to build source package: %v", err))
		http.Error(w, e.Error(), 500)
		return
	}

	archivePath := filepath.Join(builder.sharedVolumePath, archiveFilename)
	err = builder.archive(deployPkgPath, archivePath)
	if err != nil {
		e := errors.New(fmt.Sprintf("Failed to archive deployment package: %v", err))
		http.Error(w, e.Error(), 500)
		return
	}

	resp := fission.PackageBuildResponse{
		ArchiveFilename: archiveFilename,
		BuildLogs:       buildLogs,
	}

	rBody, err := json.Marshal(resp)
	if err != nil {
		e := errors.New(fmt.Sprintf("Failed to encode response body: %v", err))
		http.Error(w, e.Error(), 500)
		return
	}

	w.Header().Add("Content-Type", "application/json")
	w.Write(rBody)
	w.WriteHeader(http.StatusOK)
}

func (builder *Builder) build(command string, workDir string) (string, error) {
	// use `/bin/sh -c` to run multiple commands at the same time
	cmdStrs := []string{"-c", command}
	cmd := exec.Command("/bin/sh", cmdStrs...)
	cmd.Dir = workDir
	// set env variables for build command
	cmd.Env = append(os.Environ(),
		envSrcPkg+"="+srcPkgPath,
		envDeployPkg+"="+deployPkgPath,
	)
	buildLogs, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(buildLogs), nil
}

// archive is a function that zips directory into a zip file
func (builder *Builder) archive(src string, dst string) error {
	var files []string
	target, err := os.Stat(src)
	if err != nil {
		return err
	}
	if target.IsDir() {
		// list all
		fs, _ := ioutil.ReadDir(src)
		for _, f := range fs {
			files = append(files, filepath.Join(deployPkgPath, f.Name()))
		}
	} else {
		files = append(files, src)
	}
	return archiver.Zip.Make(dst, files)
}

// unarchive is a function that unzips a zip file to destination
func (builder *Builder) unarchive(src string, dst string) error {
	err := archiver.Zip.Open(src, dst)
	if err != nil {
		return errors.New(fmt.Sprintf("Failed to unzip source package: %v", err))
	}
	return nil
}
