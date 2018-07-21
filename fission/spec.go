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
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/fission/fission/fission/sdk"
	"github.com/fsnotify/fsnotify"
	"github.com/satori/go.uuid"
	"github.com/urfave/cli"
)

func getSpecDir(c *cli.Context) string {
	specDir := c.String("specdir")
	if len(specDir) == 0 {
		specDir = "specs"
	}
	return specDir
}

// specInit just initializes an empty spec directory and adds some
// sample YAMLs in there that might be useful.
func specInit(c *cli.Context) error {
	// Figure out spec directory
	specDir := getSpecDir(c)

	name := c.String("name")
	if len(name) == 0 {
		// come up with a name using the current dir
		dir, err := filepath.Abs(".")
		sdk.CheckErr(err, "get current working directory")
		basename := filepath.Base(dir)
		name = sdk.KubifyName(basename)
	}

	// Create spec dir
	fmt.Printf("Creating fission spec directory '%v'\n", specDir)
	err := os.MkdirAll(specDir, 0755)
	sdk.CheckErr(err, fmt.Sprintf("create spec directory '%v'", specDir))

	// Add a bit of documentation to the spec dir here
	err = ioutil.WriteFile(filepath.Join(specDir, "README"), []byte(sdk.SPEC_README), 0644)
	if err != nil {
		return err
	}

	// Write the deployment config
	dc := sdk.DeploymentConfig{
		TypeMeta: sdk.TypeMeta{
			APIVersion: sdk.SPEC_API_VERSION,
			Kind:       "DeploymentConfig",
		},
		Name: name,

		// All resources will be annotated with the UID when they're created. This allows
		// us to be idempotent, as well as to delete resources when their specs are
		// removed.
		UID: uuid.NewV4().String(),
	}
	err = sdk.WriteDeploymentConfig(specDir, &dc)
	sdk.CheckErr(err, "write deployment config")

	// Other possible things to do here:
	// - add example specs to the dir to make it easy to manually
	//   add new ones

	return nil
}

// specValidate parses a set of specs and checks for references to
// resources that don't exist.
func specValidate(c *cli.Context) error {
	// this will error on parse errors and on duplicates
	specDir := getSpecDir(c)
	fr, err := sdk.ReadSpecs(specDir)
	sdk.CheckErr(err, "read specs")

	// this does the rest of the checks, like dangling refs
	err = fr.Validate()
	if err != nil {
		fmt.Printf("Error validating specs: %v", err)
	}

	return nil
}

// specApply compares the specs in the spec/config/ directory to the
// deployed resources on the cluster, and reconciles the differences
// by creating, updating or deleting resources on the cluster.
//
// specApply is idempotent.
//
// specApply is *not* transactional -- if the user hits Ctrl-C, or their laptop dies
// etc, while doing an apply, they will get a partially applied deployment.  However,
// they can retry their apply command once they're back online.
func specApply(c *cli.Context) error {
	fclient := sdk.GetClient(c.GlobalString("server"))
	specDir := getSpecDir(c)

	deleteResources := c.Bool("delete")
	watchResources := c.Bool("watch")
	waitForBuild := c.Bool("wait")

	var watcher *fsnotify.Watcher
	var pbw *sdk.PackageBuildWatcher

	if watchResources || waitForBuild {
		// init package build watcher
		pbw = sdk.MakePackageBuildWatcher(fclient)
	}

	if watchResources {
		var err error
		watcher, err = fsnotify.NewWatcher()
		sdk.CheckErr(err, "create file watcher")

		// add watches
		rootDir := filepath.Clean(specDir + "/..")
		err = filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
			sdk.CheckErr(err, "scan project files")

			if sdk.IgnoreFile(path) {
				return nil
			}
			err = watcher.Add(path)
			sdk.CheckErr(err, fmt.Sprintf("watch path %v", path))
			return nil
		})
		sdk.CheckErr(err, "scan files to watch")
	}

	for {
		// read all specs
		fr, err := sdk.ReadSpecs(specDir)
		sdk.CheckErr(err, "read specs")

		// validate
		err = fr.Validate()
		sdk.CheckErr(err, "validate specs")

		// make changes to the cluster based on the specs
		pkgMetas, as, err := sdk.ApplyResources(fclient, specDir, fr, deleteResources)
		sdk.CheckErr(err, "apply specs")
		sdk.PrintApplyStatus(as)

		if watchResources || waitForBuild {
			// watch package builds
			pbw.AddPackages(pkgMetas)
		}

		ctx, pkgWatchCancel := context.WithCancel(context.Background())

		if watchResources {
			// if we're watching for files, we don't need to wait for builds to complete
			go pbw.Watch(ctx)
		} else if waitForBuild {
			// synchronously wait for build if --wait was specified
			pbw.Watch(ctx)
		}

		if !watchResources {
			pkgWatchCancel()
			break
		}

		// listen for file watch events
		fmt.Println("Watching files for changes...")

	waitloop:
		for {
			select {
			case e := <-watcher.Events:
				if sdk.IgnoreFile(e.Name) {
					continue waitloop
				}

				fmt.Printf("Noticed a file change, reapplying specs...\n")

				// Builds that finish after this cancellation will be
				// printed in the next watchPackageBuildStatus call.
				pkgWatchCancel()

				err = sdk.WaitForFileWatcherToSettleDown(watcher)
				sdk.CheckErr(err, "watching files")

				break waitloop
			case err := <-watcher.Errors:
				sdk.CheckErr(err, "watching files")
			}
		}
	}
	return nil
}

// specDestroy destroys everything in the spec.
func specDestroy(c *cli.Context) error {
	fclient := sdk.GetClient(c.GlobalString("server"))

	// get specdir
	specDir := getSpecDir(c)

	// read everything
	fr, err := sdk.ReadSpecs(specDir)
	sdk.CheckErr(err, "read specs")

	// set desired state to nothing, but keep the UID so "apply" can find it
	emptyFr := sdk.FissionResources{}
	emptyFr.DeploymentConfig = fr.DeploymentConfig

	// "apply" the empty state
	_, _, err = sdk.ApplyResources(fclient, specDir, &emptyFr, true)
	sdk.CheckErr(err, "delete resources")

	return nil
}

// specHelm creates a helm chart from a spec directory and a
// deployment config.
func specHelm(c *cli.Context) error {
	return nil
}
