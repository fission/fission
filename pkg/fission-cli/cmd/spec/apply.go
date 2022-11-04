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
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-git/go-git/v5"
	"github.com/mholt/archiver/v3"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	pkgutil "github.com/fission/fission/pkg/fission-cli/cmd/package/util"
	spectypes "github.com/fission/fission/pkg/fission-cli/cmd/spec/types"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/utils"
)

type ApplySubCommand struct {
	cmd.CommandActioner
}

// Apply compares the specs in the spec/config/ directory to the
// deployed resources on the cluster, and reconciles the differences
// by creating, updating or deleting resources on the cluster.
//
// Apply is idempotent.
//
// Apply is *not* transactional -- if the user hits Ctrl-C, or their laptop dies
// etc, while doing an apply, they will get a partially applied deployment.  However,
// they can retry their apply command once they're back online.
func Apply(input cli.Input) error {
	return (&ApplySubCommand{}).do(input)
}

func (opts *ApplySubCommand) do(input cli.Input) error {
	return opts.run(input)

}

// insertNamespace inserts the Namespace value if it was not provided at the time of `spec save`.
// we make sure that all component of a resource should be present in the same Namespace. i.e.
// Function's env and package should be present in same namespace
func (opts *ApplySubCommand) insertNamespace(input cli.Input, fr *FissionResources) error {

	result := utils.MultiErrorWithFormat()
	_, currentNS, err := util.GetResourceNamespace(input, flagkey.NamespaceEnvironment)
	if err != nil {
		return fv1.AggregateValidationErrors("Environment", err)
	}

	for i := range fr.Functions {
		if fr.Functions[i].Namespace == "" || input.Bool(flagkey.ForceNamespace) {
			fr.Functions[i].Namespace = currentNS
			fr.Functions[i].Spec.Package.PackageRef.Namespace = currentNS
			fr.Functions[i].Spec.Environment.Namespace = currentNS
			for j := range fr.Functions[i].Spec.ConfigMaps {
				fr.Functions[i].Spec.ConfigMaps[j].Namespace = currentNS
			}
			for j := range fr.Functions[i].Spec.Secrets {
				fr.Functions[i].Spec.Secrets[j].Namespace = currentNS
			}
		}
	}
	for i := range fr.Environments {
		if fr.Environments[i].Namespace == "" || input.Bool(flagkey.ForceNamespace) {
			fr.Environments[i].Namespace = currentNS
		}
	}
	for i := range fr.Packages {
		if fr.Packages[i].Namespace == "" || input.Bool(flagkey.ForceNamespace) {
			fr.Packages[i].Namespace = currentNS
			fr.Packages[i].Spec.Environment.Namespace = currentNS
			fr.Packages[i].ObjectMeta.Namespace = currentNS
		}
	}
	for i := range fr.HttpTriggers {
		if fr.HttpTriggers[i].Namespace == "" || input.Bool(flagkey.ForceNamespace) {
			fr.HttpTriggers[i].Namespace = currentNS
		}
	}
	for i := range fr.MessageQueueTriggers {
		if fr.MessageQueueTriggers[i].Namespace == "" || input.Bool(flagkey.ForceNamespace) {
			fr.MessageQueueTriggers[i].Namespace = currentNS
		}
	}
	for i := range fr.TimeTriggers {
		if fr.TimeTriggers[i].Namespace == "" || input.Bool(flagkey.ForceNamespace) {
			fr.TimeTriggers[i].Namespace = currentNS
		}
	}
	for i := range fr.KubernetesWatchTriggers {
		if fr.KubernetesWatchTriggers[i].Namespace == "" || input.Bool(flagkey.ForceNamespace) {
			fr.KubernetesWatchTriggers[i].Namespace = currentNS
		}
	}

	return result.ErrorOrNil()
}

func (opts *ApplySubCommand) run(input cli.Input) error {
	specDir := util.GetSpecDir(input)
	specIgnore := util.GetSpecIgnore(input)
	applyCommitLabel := input.Bool(flagkey.SpecApplyCommitLabel)
	deleteResources := input.Bool(flagkey.SpecDelete)
	watchResources := input.Bool(flagkey.SpecWatch)
	waitForBuild := input.Bool(flagkey.SpecWait)
	validateSpecs := util.GetValidationFlag(input)

	var watcher *fsnotify.Watcher
	var pbw *packageBuildWatcher

	if watchResources || waitForBuild {
		// init package build watcher
		pbw = makePackageBuildWatcher(opts.Client())
	}

	if watchResources {
		var err error
		watcher, err = fsnotify.NewWatcher()
		if err != nil {
			return errors.Wrap(err, "error creating file watcher")
		}

		// add watches
		rootDir := filepath.Clean(specDir + "/..")
		err = filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return errors.Wrap(err, "error scanning project files")
			}

			if ignoreFile(path) {
				return nil
			}

			err = watcher.Add(path)
			if err != nil {
				return errors.Wrap(err, fmt.Sprintf("error watching path %v", path))
			}
			return nil
		})
		if err != nil {
			return errors.Wrap(err, "error scanning files to watch")
		}
	}

	for {
		// read all specs
		fr, err := ReadSpecs(specDir, specIgnore, applyCommitLabel)
		if err != nil {
			return errors.Wrap(err, "error reading specs")
		}

		err = opts.insertNamespace(input, fr)
		if err != nil {
			return errors.Wrap(err, "error reading specs")
		}

		if validateSpecs {
			err = validateForApply(input, fr)
			if err != nil {
				return errors.Wrap(err, "abort applying resources")
			}
		}

		err = warnIfDirtyWorkTree(filepath.Clean(specDir + "/.."))
		if err != nil {
			console.Warn(err.Error())
		}

		// make changes to the cluster based on the specs
		pkgMetas, as, err := applyResources(input, opts.Client(), specDir, fr, deleteResources, input.Bool(flagkey.SpecAllowConflicts))
		if err != nil {
			return errors.Wrap(err, "error applying specs")
		}
		printApplyStatus(as)

		if watchResources || waitForBuild {
			// watch package builds
			pbw.addPackages(pkgMetas)
		}

		ctx, pkgWatchCancel := context.WithCancel(input.Context())

		if watchResources {
			// if we're watching for files, we don't need to wait for builds to complete
			go pbw.watch(ctx)
		} else if waitForBuild {
			// synchronously wait for build if --wait was specified
			pbw.watch(ctx)
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
				if ignoreFile(e.Name) {
					continue waitloop
				}
				fmt.Printf("Noticed a file change, reapplying specs...\n")

				// Builds that finish after this cancellation will be
				// printed in the next watchPackageBuildStatus call.
				pkgWatchCancel()

				err = waitForFileWatcherToSettleDown(watcher)
				if err != nil {
					return errors.Wrap(err, "error watching files")
				}
				break waitloop

			case err := <-watcher.Errors:
				pkgWatchCancel()

				if err != nil {
					return errors.Wrap(err, "error watching files")
				}
			}
		}
	}

	return nil
}

func warnIfDirtyWorkTree(path string) error {
	repo, err := git.PlainOpen(path)
	if err != nil {
		console.Info("Spec doesn't belong to Git Tree.")
		return nil
	}

	workTree, err := repo.Worktree()
	if err != nil {
		return err
	}

	status, err := workTree.Status()
	if err != nil {
		return err
	}

	if !status.IsClean() {
		console.Warn("Worktree is not clean, please ensure you have committed the changes to git.")
	}

	return nil
}

func ignoreFile(path string) bool {
	return (strings.Contains(path, "/.#") || // editor autosave files
		strings.HasSuffix(path, "~")) // editor backups, usually
}

func waitForFileWatcherToSettleDown(watcher *fsnotify.Watcher) error {
	// Wait a bit for things to settle down in case a bunch of
	// files changed; also drain all events that queue up during
	// the wait interval.
	time.Sleep(500 * time.Millisecond)
	for {
		select {
		case <-watcher.Events:
			time.Sleep(200 * time.Millisecond)
			continue
		case err := <-watcher.Errors:
			return err
		default:
			return nil
		}
	}
}

// printApplyStatus prints a summary of what changed on the
// cluster as the result of a spec apply operation.
func printApplyStatus(applyStatus map[string]ResourceApplyStatus) {
	changed := false
	for typ, ras := range applyStatus {
		n := len(ras.Created)
		if n > 0 {
			changed = true
			fmt.Printf("%v %v created: %v\n", n, pluralize(n, typ), strings.Join(metadataNames(ras.Created), ", "))
		}
		n = len(ras.Updated)
		if n > 0 {
			changed = true
			fmt.Printf("%v %v updated: %v\n", n, pluralize(n, typ), strings.Join(metadataNames(ras.Updated), ", "))
		}
		n = len(ras.Deleted)
		if n > 0 {
			changed = true
			fmt.Printf("%v %v deleted: %v\n", n, pluralize(n, typ), strings.Join(metadataNames(ras.Deleted), ", "))
		}
	}

	if !changed {
		fmt.Println("Everything up to date.")
	}
}

// metadataNames extracts a slice of names from a slice of object metadata.
func metadataNames(ms []*metav1.ObjectMeta) []string {
	s := make([]string, len(ms))
	for i, m := range ms {
		s[i] = m.Name
	}
	return s
}

// pluralize returns the plural of word if num is zero or more than one.
func pluralize(num int, word string) string {
	if num == 1 {
		return word
	}
	return word + "s"
}

// applyArchives figures out the set of archives that need to be uploaded, and uploads them.
func applyArchives(input cli.Input, fclient cmd.Client, specDir string, fr *FissionResources) error {

	// archive:// URL -> archive map.
	archiveFiles := make(map[string]fv1.Archive)

	// We'll first populate archiveFiles with references to local files, and then modify it to
	// point at archive URLs.

	// create archives locally and calculate checksums
	for _, aus := range fr.ArchiveUploadSpecs {
		ar, err := localArchiveFromSpec(specDir, &aus)
		if err != nil {
			return err
		}
		archiveUrl := fmt.Sprintf("%v%v", ARCHIVE_URL_PREFIX, aus.Name)
		archiveFiles[archiveUrl] = *ar
	}

	// get list of packages, make content-indexed map of available archives
	availableArchives := make(map[string]string) // (sha256 -> url)
	pkgs, err := fclient.FissionClientSet.CoreV1().Packages(metav1.NamespaceAll).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, pkg := range pkgs.Items {
		for _, ar := range []fv1.Archive{pkg.Spec.Source, pkg.Spec.Deployment} {
			if ar.Type == fv1.ArchiveTypeUrl && len(ar.URL) > 0 {
				availableArchives[ar.Checksum.Sum] = ar.URL
			}
		}
	}

	// upload archives that we need to, updating the map
	for name, ar := range archiveFiles {
		if ar.Type == fv1.ArchiveTypeLiteral {
			continue
		}
		// does the archive exist already?
		if url, ok := availableArchives[ar.Checksum.Sum]; ok {
			fmt.Printf("archive %v exists, not uploading\n", name)
			ar.URL = url
			archiveFiles[name] = ar
		} else {
			// doesn't exist, upload
			fmt.Printf("uploading archive %v\n", name)
			// ar.URL is actually a local filename at this stage
			uploadedAr, err := pkgutil.UploadArchiveFile(input.Context(), fclient, ar.URL)
			if err != nil {
				return err
			}
			archiveFiles[name] = *uploadedAr
		}
	}

	// resolve references to urls in packages to be applied
	for i := range fr.Packages {
		for _, ar := range []*fv1.Archive{&fr.Packages[i].Spec.Source, &fr.Packages[i].Spec.Deployment} {
			if strings.HasPrefix(ar.URL, ARCHIVE_URL_PREFIX) {
				availableAr, ok := archiveFiles[ar.URL]
				if !ok {
					return errors.Errorf("unknown archive name %v", strings.TrimPrefix(ar.URL, ARCHIVE_URL_PREFIX))
				}
				ar.Type = availableAr.Type
				ar.Literal = availableAr.Literal
				ar.URL = availableAr.URL
				ar.Checksum = availableAr.Checksum
			}
		}
	}
	return nil
}

// applyResources applies the given set of fission resources.
func applyResources(input cli.Input, fclient cmd.Client, specDir string, fr *FissionResources, delete bool, specAllowConflicts bool) (map[string]metav1.ObjectMeta, map[string]ResourceApplyStatus, error) {

	applyStatus := make(map[string]ResourceApplyStatus)

	// upload archives that need to be uploaded. Changes archive references in fr.Packages.
	err := applyArchives(input, fclient, specDir, fr)
	if err != nil {
		return nil, nil, err
	}

	_, ras, err := applyEnvironments(input.Context(), fclient, fr, delete, specAllowConflicts)
	if err != nil {
		return nil, nil, errors.Wrap(err, "environment apply failed")
	}
	applyStatus["environment"] = *ras

	pkgMeta, ras, err := applyPackages(input.Context(), fclient, fr, delete, specAllowConflicts)
	if err != nil {
		return nil, nil, errors.Wrap(err, "package apply failed")
	}
	applyStatus["package"] = *ras

	// Each reference to a package from a function must contain the resource version
	// of the package. This ensures that various caches can invalidate themselves
	// when the package changes.
	for i, f := range fr.Functions {
		if f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypeContainer {
			continue
		}
		k := mapKey(&metav1.ObjectMeta{
			Namespace: f.Spec.Package.PackageRef.Namespace,
			Name:      f.Spec.Package.PackageRef.Name,
		})
		m, ok := pkgMeta[k]
		if !ok {
			// the function references a package that doesn't exist in the
			// spec. It may exist outside the spec, but we're going to treat
			// that as an error, so that we encourage self-contained specs.
			// Is there a good use case for non-self contained specs?
			return nil, nil, errors.Errorf("function %v/%v references package %v/%v, which doesn't exist in the specs",
				f.ObjectMeta.Namespace, f.ObjectMeta.Name, f.Spec.Package.PackageRef.Namespace, f.Spec.Package.PackageRef.Name)
		}
		fr.Functions[i].Spec.Package.PackageRef.ResourceVersion = m.ResourceVersion
	}

	_, ras, err = applyFunctions(input.Context(), fclient, fr, delete, specAllowConflicts)
	if err != nil {
		return nil, nil, errors.Wrap(err, "function apply failed")
	}
	applyStatus["function"] = *ras

	_, ras, err = applyHTTPTriggers(input.Context(), fclient, fr, delete, specAllowConflicts)
	if err != nil {
		return nil, nil, errors.Wrap(err, "HTTPTrigger apply failed")
	}
	applyStatus["HTTPTrigger"] = *ras

	_, ras, err = applyKubernetesWatchTriggers(input.Context(), fclient, fr, delete, specAllowConflicts)
	if err != nil {
		return nil, nil, errors.Wrap(err, "KubernetesWatchTrigger apply failed")
	}
	applyStatus["KubernetesWatchTrigger"] = *ras

	_, ras, err = applyTimeTriggers(input.Context(), fclient, fr, delete, specAllowConflicts)
	if err != nil {
		return nil, nil, errors.Wrap(err, "TimeTrigger apply failed")
	}
	applyStatus["TimeTrigger"] = *ras

	_, ras, err = applyMessageQueueTriggers(input.Context(), fclient, fr, delete, specAllowConflicts)
	if err != nil {
		return nil, nil, errors.Wrap(err, "MessageQueueTrigger apply failed")
	}
	applyStatus["MessageQueueTrigger"] = *ras

	return pkgMeta, applyStatus, nil
}

// localArchiveFromSpec creates an archive on the local filesystem from the given spec,
// and returns its path and checksum.
func localArchiveFromSpec(specDir string, aus *spectypes.ArchiveUploadSpec) (*fv1.Archive, error) {
	// get root dir
	var rootDir string

	if len(aus.RootDir) == 0 {
		rootDir = filepath.Clean(specDir + "/..")
	} else {
		rootDir = aus.RootDir
	}

	// get a list of files from the include/exclude globs.
	//
	// XXX if there are lots of globs it's probably more efficient
	// to do a filepath.Walk and call path.Match on each path...
	files := make([]string, 0)

	//checking if file is a zip
	if match, _ := utils.IsZip(aus.IncludeGlobs[0]); match && len(aus.IncludeGlobs) == 1 {
		files = append(files, aus.IncludeGlobs[0])
	} else {
		for _, relativeGlob := range aus.IncludeGlobs {
			absGlob := filepath.Join(rootDir, relativeGlob)
			console.Verbose(2, "try to find globs in path '%v'", absGlob)
			fs, err := utils.FindAllGlobs(absGlob)
			if err != nil {
				return nil, errors.Wrapf(err, "Invalid glob in archive %v: %v", aus.Name, relativeGlob)
			}
			files = append(files, fs...)
		}
	}

	if len(files) == 0 {
		return nil, errors.Errorf("archive '%v' is empty", aus.Name)
	}

	// if it's just one file, use its path directly
	var archiveFileName string
	var isSingleFile bool

	if len(files) == 1 {
		// check whether a path destination is file or directory
		f, err := os.Stat(files[0])
		if err != nil {
			return nil, err
		}
		if !f.IsDir() {
			isSingleFile = true
			archiveFileName = files[0]
		}
	}

	if len(files) > 1 || !isSingleFile {
		// Generate archive name with .zip extension and pack all files under it.
		archiveFile, err := os.CreateTemp("", fmt.Sprintf("fission-archive-%v-*.zip", aus.Name))
		if err != nil {
			return nil, err
		}
		archiveFileName = archiveFile.Name()

		//This instance is required to allow overwriting and not changing DefaultZip
		zipOverwrite := archiver.Zip{OverwriteExisting: true}
		err = zipOverwrite.Archive(files, archiveFileName)
		if err != nil {
			return nil, err
		}
	}

	size, err := utils.FileSize(archiveFileName)
	if err != nil {
		return nil, err
	}

	// figure out if we're making a literal or a URL-based archive
	if size < fv1.ArchiveLiteralSizeLimit {
		contents, err := pkgutil.GetContents(archiveFileName)
		if err != nil {
			return nil, err
		}
		return &fv1.Archive{
			Type:    fv1.ArchiveTypeLiteral,
			Literal: contents,
		}, nil
	} else {
		// checksum
		csum, err := utils.GetFileChecksum(archiveFileName)
		if err != nil {
			return nil, errors.Errorf("failed to calculate archive checksum for %v (%v): %v", aus.Name, archiveFileName, err)
		}

		// archive object
		return &fv1.Archive{
			Type: fv1.ArchiveTypeUrl,
			// we should be actually be adding a "file://" prefix, but this archive is only an
			// intermediate step, so just the path works fine.
			URL:      archiveFileName,
			Checksum: *csum,
		}, nil

	}
}

func mapKey(m *metav1.ObjectMeta) string {
	return fmt.Sprintf("%v:%v", m.Namespace, m.Name)
}

func applyDeploymentConfig(m *metav1.ObjectMeta, fr *FissionResources) {
	if m.Annotations == nil {
		m.Annotations = make(map[string]string)
	}
	m.Annotations[FISSION_DEPLOYMENT_NAME_KEY] = fr.DeploymentConfig.Name
	m.Annotations[FISSION_DEPLOYMENT_UID_KEY] = fr.DeploymentConfig.UID
}

func hasDeploymentConfig(m *metav1.ObjectMeta, fr *FissionResources) bool {
	if m.Annotations == nil {
		return false
	}
	uid, ok := m.Annotations[FISSION_DEPLOYMENT_UID_KEY]
	if ok && uid == fr.DeploymentConfig.UID {
		return true
	}
	return false
}

func waitForPackageBuild(ctx context.Context, fclient cmd.Client, pkg *fv1.Package) (*fv1.Package, error) {
	start := time.Now()
	for {
		if pkg.Status.BuildStatus != fv1.BuildStatusRunning {
			return pkg, nil
		}
		if time.Since(start) > 5*time.Minute {
			return nil, errors.Errorf("package %v has been building for a while, giving up on waiting for it", pkg.ObjectMeta.Name)
		}

		// TODO watch instead
		time.Sleep(time.Second)

		var err error
		pkg, err = fclient.FissionClientSet.CoreV1().Packages(pkg.ObjectMeta.Namespace).Get(ctx, pkg.ObjectMeta.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
	}
}

func applyPackages(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	// get list
	allObjs, err := fclient.FissionClientSet.CoreV1().Packages(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}

	// filter
	objs := make([]fv1.Package, 0)
	if specAllowConflicts {
		objs = allObjs.Items
	} else {
		for _, o := range allObjs.Items {
			if hasDeploymentConfig(&o.ObjectMeta, fr) {
				objs = append(objs, o)
			}
		}
	}

	// index
	existent := make(map[string]fv1.Package)
	for _, obj := range objs {
		existent[mapKey(&obj.ObjectMeta)] = obj
	}
	metadataMap := make(map[string]metav1.ObjectMeta)

	// desired set. used to compute the set to delete.
	desired := make(map[string]bool)

	var ras ResourceApplyStatus

	// create or update desired state
	for _, o := range fr.Packages {
		// apply deploymentConfig so we can find our objects on future apply invocations
		applyDeploymentConfig(&o.ObjectMeta, fr)
		console.Verbose(2, fmt.Sprintf("Package is here '%s','%s','%s','%s'", o.Namespace, o.Name, o.Spec.Environment.Namespace, o.Spec.Environment.Name))

		// index desired state
		desired[mapKey(&o.ObjectMeta)] = true

		// exists?
		existingObj, ok := existent[mapKey(&o.ObjectMeta)]
		if ok {
			// ok, a resource with the same name exists, is it the same?
			keep := false
			if reflect.DeepEqual(existingObj.Spec, o.Spec) {
				keep = true
			} else if reflect.DeepEqual(existingObj.Spec.Environment, o.Spec.Environment) &&
				!reflect.DeepEqual(existingObj.Spec.Source, fv1.Archive{}) &&
				reflect.DeepEqual(existingObj.Spec.Source, o.Spec.Source) &&
				existingObj.Spec.BuildCommand == o.Spec.BuildCommand {

				keep = true
			}

			if keep && isObjectMetaEqual(existingObj.ObjectMeta, o.ObjectMeta) && existingObj.Status.BuildStatus == fv1.BuildStatusSucceeded {
				// nothing to do on the server
				metadataMap[mapKey(&o.ObjectMeta)] = existingObj.ObjectMeta
			} else {
				// update
				o.ObjectMeta.ResourceVersion = existingObj.ObjectMeta.ResourceVersion
				// We may be racing against the package builder to update the
				// package (a previous version might have been getting built).  So,
				// wait for the package to have a non-running build status.
				pkg, err := waitForPackageBuild(ctx, fclient, &o)
				if err != nil {
					// log and ignore
					console.Warn(fmt.Sprintf("Error waiting for package '%v' build, ignoring", o.ObjectMeta.Name))
					pkg = &o
				}

				// update status in order to rebuild the package again
				if pkg.Status.BuildStatus == fv1.BuildStatusFailed {
					pkg.Status.BuildStatus = fv1.BuildStatusPending
				}

				newmeta, err := fclient.FissionClientSet.CoreV1().Packages(pkg.ObjectMeta.Namespace).Update(ctx, pkg, metav1.UpdateOptions{})
				if err != nil {
					return nil, nil, err
					// TODO check for resourceVersion conflict errors and retry
				}
				ras.Updated = append(ras.Updated, &newmeta.ObjectMeta)
				// keep track of metadata in case we need to create a reference to it
				metadataMap[mapKey(&o.ObjectMeta)] = newmeta.ObjectMeta
			}
		} else {

			// create
			newmeta, err := fclient.FissionClientSet.CoreV1().Packages(o.ObjectMeta.Namespace).Create(ctx, &o, metav1.CreateOptions{})
			if err != nil {
				return nil, nil, err
			}
			ras.Created = append(ras.Created, &newmeta.ObjectMeta)
			metadataMap[mapKey(&o.ObjectMeta)] = newmeta.ObjectMeta
		}
	}

	// deletes
	if delete {
		// objs is already filtered with our UID
		for _, o := range objs {
			_, wanted := desired[mapKey(&o.ObjectMeta)]
			if !wanted {
				err := fclient.FissionClientSet.CoreV1().Packages(o.ObjectMeta.Namespace).Delete(ctx, o.ObjectMeta.Name, metav1.DeleteOptions{})
				if err != nil {
					return nil, nil, err
				}
				ras.Deleted = append(ras.Deleted, &o.ObjectMeta)
				fmt.Printf("Deleted %v %v/%v\n", o.TypeMeta.Kind, o.ObjectMeta.Namespace, o.ObjectMeta.Name)
			}
		}
	}

	return metadataMap, &ras, nil
}

func applyFunctions(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	// get list
	allObjs, err := fclient.FissionClientSet.CoreV1().Functions(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}

	// filter
	objs := make([]fv1.Function, 0)
	if specAllowConflicts {
		objs = allObjs.Items
	} else {
		for _, o := range allObjs.Items {
			if hasDeploymentConfig(&o.ObjectMeta, fr) {
				objs = append(objs, o)
			}
		}
	}

	// index
	existent := make(map[string]fv1.Function)
	for _, obj := range objs {
		existent[mapKey(&obj.ObjectMeta)] = obj
	}
	metadataMap := make(map[string]metav1.ObjectMeta)

	// desired set. used to compute the set to delete.
	desired := make(map[string]bool)

	var ras ResourceApplyStatus

	// create or update desired state
	for _, o := range fr.Functions {
		// apply deploymentConfig so we can find our objects on future apply invocations
		applyDeploymentConfig(&o.ObjectMeta, fr)

		// index desired state
		desired[mapKey(&o.ObjectMeta)] = true

		// exists?
		existingObj, ok := existent[mapKey(&o.ObjectMeta)]
		if ok {
			// ok, a resource with the same name exists, is it the same?
			if isObjectMetaEqual(existingObj.ObjectMeta, o.ObjectMeta) && reflect.DeepEqual(existingObj.Spec, o.Spec) {
				// nothing to do on the server
				metadataMap[mapKey(&o.ObjectMeta)] = existingObj.ObjectMeta
			} else {
				// update
				o.ObjectMeta.ResourceVersion = existingObj.ObjectMeta.ResourceVersion
				newmeta, err := fclient.FissionClientSet.CoreV1().Functions(o.ObjectMeta.Namespace).Update(ctx, &o, metav1.UpdateOptions{})
				if err != nil {
					return nil, nil, err
				}
				ras.Updated = append(ras.Updated, &newmeta.ObjectMeta)
				// keep track of metadata in case we need to create a reference to it
				metadataMap[mapKey(&o.ObjectMeta)] = newmeta.ObjectMeta
			}
		} else {
			// create
			newmeta, err := fclient.FissionClientSet.CoreV1().Functions(o.ObjectMeta.Namespace).Create(ctx, &o, metav1.CreateOptions{})
			if err != nil {
				return nil, nil, err
			}
			ras.Created = append(ras.Created, &newmeta.ObjectMeta)
			metadataMap[mapKey(&o.ObjectMeta)] = newmeta.ObjectMeta
		}
	}

	// deletes
	if delete {
		// objs is already filtered with our UID
		for _, o := range objs {
			_, wanted := desired[mapKey(&o.ObjectMeta)]
			if !wanted {
				err := fclient.FissionClientSet.CoreV1().Functions(o.ObjectMeta.Namespace).Delete(ctx, o.ObjectMeta.Name, metav1.DeleteOptions{})
				if err != nil {
					return nil, nil, err
				}
				ras.Deleted = append(ras.Deleted, &o.ObjectMeta)
				fmt.Printf("Deleted %v %v/%v\n", o.TypeMeta.Kind, o.ObjectMeta.Namespace, o.ObjectMeta.Name)
			}
		}
	}

	return metadataMap, &ras, nil
}

func applyEnvironments(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	// get list
	allObjs, err := fclient.FissionClientSet.CoreV1().Environments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}

	// filter
	objs := make([]fv1.Environment, 0)
	if specAllowConflicts {
		objs = allObjs.Items
	} else {
		for _, o := range allObjs.Items {
			if hasDeploymentConfig(&o.ObjectMeta, fr) {
				objs = append(objs, o)
			}
		}
	}

	// index
	existent := make(map[string]fv1.Environment)
	for _, obj := range objs {
		existent[mapKey(&obj.ObjectMeta)] = obj
	}
	metadataMap := make(map[string]metav1.ObjectMeta)

	// desired set. used to compute the set to delete.
	desired := make(map[string]bool)

	var ras ResourceApplyStatus

	// create or update desired state
	for _, o := range fr.Environments {
		// apply deploymentConfig so we can find our objects on future apply invocations
		applyDeploymentConfig(&o.ObjectMeta, fr)

		// index desired state
		desired[mapKey(&o.ObjectMeta)] = true

		// exists?
		existingObj, ok := existent[mapKey(&o.ObjectMeta)]
		if ok {
			// ok, a resource with the same name exists, is it the same?
			if isObjectMetaEqual(existingObj.ObjectMeta, o.ObjectMeta) && reflect.DeepEqual(existingObj.Spec, o.Spec) {
				// nothing to do on the server
				metadataMap[mapKey(&o.ObjectMeta)] = existingObj.ObjectMeta
			} else {
				// update
				o.ObjectMeta.ResourceVersion = existingObj.ObjectMeta.ResourceVersion
				newmeta, err := fclient.FissionClientSet.CoreV1().Environments(o.ObjectMeta.Namespace).Update(ctx, &o, metav1.UpdateOptions{})
				if err != nil {
					return nil, nil, err
				}
				ras.Updated = append(ras.Updated, &newmeta.ObjectMeta)
				// keep track of metadata in case we need to create a reference to it
				metadataMap[mapKey(&o.ObjectMeta)] = newmeta.ObjectMeta
			}
		} else {
			// create
			newmeta, err := fclient.FissionClientSet.CoreV1().Environments(o.ObjectMeta.Namespace).Create(ctx, &o, metav1.CreateOptions{})
			if err != nil {
				return nil, nil, err
			}
			ras.Created = append(ras.Created, &newmeta.ObjectMeta)
			metadataMap[mapKey(&o.ObjectMeta)] = newmeta.ObjectMeta
		}
	}

	// deletes
	if delete {
		// objs is already filtered with our UID
		for _, o := range objs {
			_, wanted := desired[mapKey(&o.ObjectMeta)]
			if !wanted {
				err := fclient.FissionClientSet.CoreV1().Environments(o.ObjectMeta.Namespace).Delete(ctx, o.ObjectMeta.Namespace, metav1.DeleteOptions{})
				if err != nil {
					return nil, nil, err
				}
				ras.Deleted = append(ras.Deleted, &o.ObjectMeta)
				fmt.Printf("Deleted %v %v/%v\n", o.TypeMeta.Kind, o.ObjectMeta.Namespace, o.ObjectMeta.Name)
			}
		}
	}

	return metadataMap, &ras, nil
}

func applyHTTPTriggers(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	// get list
	allObjs, err := fclient.FissionClientSet.CoreV1().HTTPTriggers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}

	// filter
	objs := make([]fv1.HTTPTrigger, 0)
	if specAllowConflicts {
		objs = allObjs.Items
	} else {
		for _, o := range allObjs.Items {
			if hasDeploymentConfig(&o.ObjectMeta, fr) {
				objs = append(objs, o)
			}
		}
	}

	// index
	existent := make(map[string]fv1.HTTPTrigger)
	for _, obj := range objs {
		existent[mapKey(&obj.ObjectMeta)] = obj
	}
	metadataMap := make(map[string]metav1.ObjectMeta)

	// desired set. used to compute the set to delete.
	desired := make(map[string]bool)

	var ras ResourceApplyStatus

	// create or update desired state
	for _, o := range fr.HttpTriggers {
		// apply deploymentConfig so we can find our objects on future apply invocations
		applyDeploymentConfig(&o.ObjectMeta, fr)

		// index desired state
		desired[mapKey(&o.ObjectMeta)] = true

		// exists?
		existingObj, ok := existent[mapKey(&o.ObjectMeta)]
		if ok {
			// ok, a resource with the same name exists, is it the same?
			if isObjectMetaEqual(existingObj.ObjectMeta, o.ObjectMeta) && reflect.DeepEqual(existingObj.Spec, o.Spec) {
				// nothing to do on the server
				metadataMap[mapKey(&o.ObjectMeta)] = existingObj.ObjectMeta
			} else {

				err := util.CheckHTTPTriggerDuplicates(ctx, fclient, &o)
				if err != nil {
					return nil, nil, err
				}
				// update
				o.ObjectMeta.ResourceVersion = existingObj.ObjectMeta.ResourceVersion
				newmeta, err := fclient.FissionClientSet.CoreV1().HTTPTriggers(o.ObjectMeta.Namespace).Update(ctx, &o, metav1.UpdateOptions{})
				if err != nil {
					return nil, nil, err
				}
				ras.Updated = append(ras.Updated, &newmeta.ObjectMeta)
				// keep track of metadata in case we need to create a reference to it
				metadataMap[mapKey(&o.ObjectMeta)] = newmeta.ObjectMeta
			}
		} else {

			err := util.CheckHTTPTriggerDuplicates(ctx, fclient, &o)
			if err != nil {
				return nil, nil, err
			}
			// create
			newmeta, err := fclient.FissionClientSet.CoreV1().HTTPTriggers(o.ObjectMeta.Namespace).Create(ctx, &o, metav1.CreateOptions{})
			if err != nil {
				return nil, nil, err
			}
			ras.Created = append(ras.Created, &newmeta.ObjectMeta)
			metadataMap[mapKey(&o.ObjectMeta)] = newmeta.ObjectMeta
		}
	}

	// deletes
	if delete {
		// objs is already filtered with our UID
		for _, o := range objs {
			_, wanted := desired[mapKey(&o.ObjectMeta)]
			if !wanted {
				err := fclient.FissionClientSet.CoreV1().HTTPTriggers(o.ObjectMeta.Namespace).Delete(ctx, o.ObjectMeta.Name, metav1.DeleteOptions{})
				if err != nil {
					return nil, nil, err
				}
				ras.Deleted = append(ras.Deleted, &o.ObjectMeta)
				fmt.Printf("Deleted %v %v/%v\n", o.TypeMeta.Kind, o.ObjectMeta.Namespace, o.ObjectMeta.Name)
			}
		}
	}

	return metadataMap, &ras, nil
}

func applyKubernetesWatchTriggers(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	// get list
	allObjs, err := fclient.FissionClientSet.CoreV1().KubernetesWatchTriggers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}

	// filter
	objs := make([]fv1.KubernetesWatchTrigger, 0)
	if specAllowConflicts {
		objs = allObjs.Items
	} else {
		for _, o := range allObjs.Items {
			if hasDeploymentConfig(&o.ObjectMeta, fr) {
				objs = append(objs, o)
			}
		}
	}

	// index
	existent := make(map[string]fv1.KubernetesWatchTrigger)
	for _, obj := range objs {
		existent[mapKey(&obj.ObjectMeta)] = obj
	}
	metadataMap := make(map[string]metav1.ObjectMeta)

	// desired set. used to compute the set to delete.
	desired := make(map[string]bool)

	var ras ResourceApplyStatus

	// create or update desired state
	for _, o := range fr.KubernetesWatchTriggers {
		// apply deploymentConfig so we can find our objects on future apply invocations
		applyDeploymentConfig(&o.ObjectMeta, fr)

		// index desired state
		desired[mapKey(&o.ObjectMeta)] = true

		// exists?
		existingObj, ok := existent[mapKey(&o.ObjectMeta)]
		if ok {
			// ok, a resource with the same name exists, is it the same?
			if isObjectMetaEqual(existingObj.ObjectMeta, o.ObjectMeta) && reflect.DeepEqual(existingObj.Spec, o.Spec) {
				// nothing to do on the server
				metadataMap[mapKey(&o.ObjectMeta)] = existingObj.ObjectMeta
			} else {
				// update
				o.ObjectMeta.ResourceVersion = existingObj.ObjectMeta.ResourceVersion
				newmeta, err := fclient.FissionClientSet.CoreV1().KubernetesWatchTriggers(o.ObjectMeta.Namespace).Update(ctx, &o, metav1.UpdateOptions{})
				if err != nil {
					return nil, nil, err
				}
				ras.Updated = append(ras.Updated, &newmeta.ObjectMeta)
				// keep track of metadata in case we need to create a reference to it
				metadataMap[mapKey(&o.ObjectMeta)] = newmeta.ObjectMeta
			}
		} else {
			// create
			newmeta, err := fclient.FissionClientSet.CoreV1().KubernetesWatchTriggers(o.ObjectMeta.Namespace).Create(ctx, &o, metav1.CreateOptions{})
			if err != nil {
				return nil, nil, err
			}
			ras.Created = append(ras.Created, &newmeta.ObjectMeta)
			metadataMap[mapKey(&o.ObjectMeta)] = newmeta.ObjectMeta
		}
	}

	// deletes
	if delete {
		// objs is already filtered with our UID
		for _, o := range objs {
			_, wanted := desired[mapKey(&o.ObjectMeta)]
			if !wanted {
				err := fclient.FissionClientSet.CoreV1().KubernetesWatchTriggers(o.ObjectMeta.Namespace).Delete(ctx, o.ObjectMeta.Name, metav1.DeleteOptions{})
				if err != nil {
					return nil, nil, err
				}
				ras.Deleted = append(ras.Deleted, &o.ObjectMeta)
				fmt.Printf("Deleted %v %v/%v\n", o.TypeMeta.Kind, o.ObjectMeta.Namespace, o.ObjectMeta.Name)
			}
		}
	}

	return metadataMap, &ras, nil
}

func applyTimeTriggers(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	// get list
	allObjs, err := fclient.FissionClientSet.CoreV1().TimeTriggers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}

	// filter
	objs := make([]fv1.TimeTrigger, 0)
	if specAllowConflicts {
		objs = allObjs.Items
	} else {
		for _, o := range allObjs.Items {
			if hasDeploymentConfig(&o.ObjectMeta, fr) {
				objs = append(objs, o)
			}
		}
	}

	// index
	existent := make(map[string]fv1.TimeTrigger)
	for _, obj := range objs {
		existent[mapKey(&obj.ObjectMeta)] = obj
	}
	metadataMap := make(map[string]metav1.ObjectMeta)

	// desired set. used to compute the set to delete.
	desired := make(map[string]bool)

	var ras ResourceApplyStatus

	// create or update desired state
	for _, o := range fr.TimeTriggers {
		// apply deploymentConfig so we can find our objects on future apply invocations
		applyDeploymentConfig(&o.ObjectMeta, fr)

		// index desired state
		desired[mapKey(&o.ObjectMeta)] = true

		// exists?
		existingObj, ok := existent[mapKey(&o.ObjectMeta)]
		if ok {
			// ok, a resource with the same name exists, is it the same?
			if isObjectMetaEqual(existingObj.ObjectMeta, o.ObjectMeta) && reflect.DeepEqual(existingObj.Spec, o.Spec) {
				// nothing to do on the server
				metadataMap[mapKey(&o.ObjectMeta)] = existingObj.ObjectMeta
			} else {
				// update
				o.ObjectMeta.ResourceVersion = existingObj.ObjectMeta.ResourceVersion
				newmeta, err := fclient.FissionClientSet.CoreV1().TimeTriggers(o.ObjectMeta.Namespace).Update(ctx, &o, metav1.UpdateOptions{})
				if err != nil {
					return nil, nil, err
				}
				ras.Updated = append(ras.Updated, &newmeta.ObjectMeta)
				// keep track of metadata in case we need to create a reference to it
				metadataMap[mapKey(&o.ObjectMeta)] = newmeta.ObjectMeta
			}
		} else {
			// create
			newmeta, err := fclient.FissionClientSet.CoreV1().TimeTriggers(o.Namespace).Create(ctx, &o, metav1.CreateOptions{})
			if err != nil {
				return nil, nil, err
			}
			ras.Created = append(ras.Created, &newmeta.ObjectMeta)
			metadataMap[mapKey(&o.ObjectMeta)] = newmeta.ObjectMeta
		}
	}

	// deletes
	if delete {
		// objs is already filtered with our UID
		for _, o := range objs {
			_, wanted := desired[mapKey(&o.ObjectMeta)]
			if !wanted {
				err := fclient.FissionClientSet.CoreV1().TimeTriggers(o.ObjectMeta.Namespace).Delete(ctx, o.ObjectMeta.Name, metav1.DeleteOptions{})
				if err != nil {
					return nil, nil, err
				}
				ras.Deleted = append(ras.Deleted, &o.ObjectMeta)
				fmt.Printf("Deleted %v %v/%v\n", o.TypeMeta.Kind, o.ObjectMeta.Namespace, o.ObjectMeta.Name)
			}
		}
	}

	return metadataMap, &ras, nil
}

func applyMessageQueueTriggers(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	// get list
	allObjs, err := fclient.FissionClientSet.CoreV1().MessageQueueTriggers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}

	// filter
	objs := make([]fv1.MessageQueueTrigger, 0)
	if specAllowConflicts {
		objs = allObjs.Items
	} else {
		for _, o := range allObjs.Items {
			if hasDeploymentConfig(&o.ObjectMeta, fr) {
				objs = append(objs, o)
			}
		}
	}

	// index
	existent := make(map[string]fv1.MessageQueueTrigger)
	for _, obj := range objs {
		existent[mapKey(&obj.ObjectMeta)] = obj
	}
	metadataMap := make(map[string]metav1.ObjectMeta)

	// desired set. used to compute the set to delete.
	desired := make(map[string]bool)

	var ras ResourceApplyStatus

	// create or update desired state
	for _, o := range fr.MessageQueueTriggers {
		// apply deploymentConfig so we can find our objects on future apply invocations
		applyDeploymentConfig(&o.ObjectMeta, fr)

		// index desired state
		desired[mapKey(&o.ObjectMeta)] = true

		// exists?
		existingObj, ok := existent[mapKey(&o.ObjectMeta)]
		if ok {
			// ok, a resource with the same name exists, is it the same?
			if isObjectMetaEqual(existingObj.ObjectMeta, o.ObjectMeta) && reflect.DeepEqual(existingObj.Spec, o.Spec) {
				// nothing to do on the server
				metadataMap[mapKey(&o.ObjectMeta)] = existingObj.ObjectMeta
			} else {
				// update
				o.ObjectMeta.ResourceVersion = existingObj.ObjectMeta.ResourceVersion
				newmeta, err := fclient.FissionClientSet.CoreV1().MessageQueueTriggers(o.ObjectMeta.Namespace).Update(ctx, &o, metav1.UpdateOptions{})
				if err != nil {
					return nil, nil, err
				}
				ras.Updated = append(ras.Updated, &newmeta.ObjectMeta)
				// keep track of metadata in case we need to create a reference to it
				metadataMap[mapKey(&o.ObjectMeta)] = newmeta.ObjectMeta
			}
		} else {
			// create
			newmeta, err := fclient.FissionClientSet.CoreV1().MessageQueueTriggers(o.ObjectMeta.Namespace).Create(ctx, &o, metav1.CreateOptions{})
			if err != nil {
				return nil, nil, err
			}
			ras.Created = append(ras.Created, &newmeta.ObjectMeta)
			metadataMap[mapKey(&o.ObjectMeta)] = newmeta.ObjectMeta
		}
	}

	// deletes
	if delete {
		// objs is already filtered with our UID
		for _, o := range objs {
			_, wanted := desired[mapKey(&o.ObjectMeta)]
			if !wanted {
				err := fclient.FissionClientSet.CoreV1().MessageQueueTriggers(o.ObjectMeta.Namespace).Delete(ctx, o.ObjectMeta.Name, metav1.DeleteOptions{})
				if err != nil {
					return nil, nil, err
				}
				ras.Deleted = append(ras.Deleted, &o.ObjectMeta)
				fmt.Printf("Deleted %v %v/%v\n", o.TypeMeta.Kind, o.ObjectMeta.Namespace, o.ObjectMeta.Name)
			}
		}
	}

	return metadataMap, &ras, nil
}

func isObjectMetaEqual(existingObj, newObj metav1.ObjectMeta) bool {
	return reflect.DeepEqual(existingObj.Labels, newObj.Labels) && reflect.DeepEqual(existingObj.Annotations, newObj.Annotations)
}
