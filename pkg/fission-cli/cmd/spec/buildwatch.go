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

package spec

import (
	"context"
	"fmt"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/package/util"
)

type (
	// packageBuildWatcher is used to watch a set of in-progress builds.
	packageBuildWatcher struct {
		// fission client
		fclient cmd.Client
		// set of packages already printed, ensures we don't duplicate the notifications
		finished map[string]bool

		// set of metadata in the app spec.  packages outside this set should be ignored.
		pkgMeta map[string]metav1.ObjectMeta
	}
)

func makePackageBuildWatcher(fclient cmd.Client) *packageBuildWatcher {
	return &packageBuildWatcher{
		fclient:  fclient,
		finished: make(map[string]bool),
		pkgMeta:  make(map[string]metav1.ObjectMeta),
	}
}

func (w *packageBuildWatcher) addPackages(pkgMeta map[string]metav1.ObjectMeta) {
	for k, v := range pkgMeta {
		w.pkgMeta[k] = v
	}
}

func (w *packageBuildWatcher) watch(ctx context.Context) {
	for {
		// non-blocking check if we're cancelled
		select {
		case <-ctx.Done():
			return
		default:
		}

		// pull list of packages (TODO: convert to watch)
		pkgs, err := w.fclient.FissionClientSet.CoreV1().Packages(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			fmt.Printf("Getting list of packages: %v", err)
			os.Exit(1)
		}

		// find packages that (a) are in the app spec and (b) have an interesting
		// build status (either succeeded or failed; not "none")
		keepWaiting := false
		buildpkgs := make([]fv1.Package, 0)
		for _, pkg := range pkgs.Items {
			_, ok := w.pkgMeta[mapKey(&pkg.ObjectMeta)]
			if !ok {
				continue
			}
			if pkg.Status.BuildStatus == fv1.BuildStatusNone {
				continue
			}
			if pkg.Status.BuildStatus == fv1.BuildStatusPending ||
				pkg.Status.BuildStatus == fv1.BuildStatusRunning {
				keepWaiting = true
			}
			buildpkgs = append(buildpkgs, pkg)
		}

		// print package status, and error logs if any
		for _, pkg := range buildpkgs {
			k := pkgKey(&pkg)
			if _, printed := w.finished[k]; printed {
				continue
			}
			if pkg.Status.BuildStatus == fv1.BuildStatusFailed ||
				pkg.Status.BuildStatus == fv1.BuildStatusSucceeded {
				w.finished[k] = true
				fmt.Printf("------\n")
				util.PrintPackageSummary(os.Stdout, &pkg)
				fmt.Printf("------\n")
			}
			if pkg.Status.BuildStatus == fv1.BuildStatusFailed {
				os.Exit(1)
			}
		}

		// if there are no builds running, we can stop polling
		if !keepWaiting {
			return
		}
		time.Sleep(time.Second)
	}
}

func pkgKey(pkg *fv1.Package) string {
	// packages are mutable so we want to keep track of them by resource version
	return fmt.Sprintf("%v:%v:%v", pkg.ObjectMeta.Name, pkg.ObjectMeta.Namespace, pkg.ObjectMeta.ResourceVersion)
}
