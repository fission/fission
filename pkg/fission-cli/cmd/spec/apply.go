// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sCache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	pkgutil "github.com/fission/fission/pkg/fission-cli/cmd/package/util"
	spectypes "github.com/fission/fission/pkg/fission-cli/cmd/spec/types"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	typedv1 "github.com/fission/fission/pkg/generated/clientset/versioned/typed/core/v1"
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
	_, currentNS, err := opts.GetResourceNamespace(input)
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
			fr.Packages[i].Namespace = currentNS
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
	for i := range fr.Workflows {
		if fr.Workflows[i].Namespace == "" || input.Bool(flagkey.ForceNamespace) {
			fr.Workflows[i].Namespace = currentNS
		}
	}

	return nil
}

func (opts *ApplySubCommand) run(input cli.Input) error {
	specDir := util.GetSpecDir(input)
	specIgnore := util.GetSpecIgnore(input)
	applyCommitLabel := input.Bool(flagkey.SpecApplyCommitLabel)
	deleteResources := input.Bool(flagkey.SpecDelete)
	dryRun := input.Bool(flagkey.SpecApplyDryRun)
	// --watch/--wait drive package-build polling, which is meaningless when
	// nothing is applied, so they are inert under --dry-run.
	watchResources := input.Bool(flagkey.SpecWatch) && !dryRun
	waitForBuild := input.Bool(flagkey.SpecWait) && !dryRun
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
			return fmt.Errorf("error creating file watcher: %w", err)
		}

		// add watches
		rootDir := filepath.Clean(specDir + "/..")
		err = filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return fmt.Errorf("error scanning project files: %w", err)
			}

			if ignoreFile(path) {
				return nil
			}

			err = watcher.Add(path)
			if err != nil {
				return fmt.Errorf("error watching path %v: %w", path, err)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("error scanning files to watch: %w", err)
		}
	}

	for {
		// read all specs
		fr, err := ReadSpecs(specDir, specIgnore, applyCommitLabel)
		if err != nil {
			return fmt.Errorf("error reading specs: %w", err)
		}

		if validateSpecs {
			err = validateForApply(input, fr)
			if err != nil {
				return fmt.Errorf("abort applying resources: %w", err)
			}
		}

		err = opts.insertNamespace(input, fr)
		if err != nil {
			return fmt.Errorf("error inserting namespace: %w", err)
		}

		err = warnIfDirtyWorkTree(filepath.Clean(specDir + "/.."))
		if err != nil {
			console.Warn(err.Error())
		}

		// make changes to the cluster based on the specs
		pkgMetas, as, err := applyResources(input, opts.Client(), specDir, fr, deleteResources, input.Bool(flagkey.SpecAllowConflicts), dryRun)
		if err != nil {
			return fmt.Errorf("error applying specs: %w", err)
		}
		printApplyStatus(as, dryRun)

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
					return fmt.Errorf("error watching files: %w", err)
				}
				break waitloop

			case err := <-watcher.Errors:
				pkgWatchCancel()

				if err != nil {
					return fmt.Errorf("error watching files: %w", err)
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
// printApplyStatus prints the per-kind summary of an apply. When dryRun is set
// the verbs switch to "would be …" and a "(dry run - no changes made)" footer
// is appended, so the preview is unambiguous.
func printApplyStatus(applyStatus map[string]ResourceApplyStatus, dryRun bool) {
	created, updated, deleted := "created", "updated", "deleted"
	if dryRun {
		created, updated, deleted = "would be created", "would be updated", "would be deleted"
	}

	changed := false
	for typ, ras := range applyStatus {
		if n := len(ras.Created); n > 0 {
			changed = true
			fmt.Printf("%v %v %v: %v\n", n, pluralize(n, typ), created, strings.Join(metadataNames(ras.Created), ", "))
		}
		if n := len(ras.Updated); n > 0 {
			changed = true
			fmt.Printf("%v %v %v: %v\n", n, pluralize(n, typ), updated, strings.Join(metadataNames(ras.Updated), ", "))
		}
		if n := len(ras.Deleted); n > 0 {
			changed = true
			fmt.Printf("%v %v %v: %v\n", n, pluralize(n, typ), deleted, strings.Join(metadataNames(ras.Deleted), ", "))
		}
	}

	if !changed {
		fmt.Println("Everything up to date.")
	}
	if dryRun {
		fmt.Println("(dry run - no changes made)")
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
// Under dryRun the read-only work still runs — local archives are built/checksummed
// and matched against archives already on the cluster — so the resolved Package
// specs (and therefore the diff) are accurate for unchanged archives; only the
// actual upload of a new/changed archive is skipped (such a Package legitimately
// shows as a would-create/update).
func applyArchives(input cli.Input, fclient cmd.Client, specDir string, fr *FissionResources, dryRun bool) error {
	// archive:// URL -> archive map.
	archiveFiles := make(map[string]fv1.Archive)

	// We'll first populate archiveFiles with references to local files, and then modify it to
	// point at archive URLs.

	// create archives locally and calculate checksums
	for _, aus := range fr.ArchiveUploadSpecs {
		ar, err := localArchiveFromSpec(input.Context(), specDir, &aus)
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
		} else if dryRun {
			// new/changed archive: a real apply would upload it and the owning
			// Package would be created/updated. Skip the upload (a mutation) and
			// leave the local reference so the Package shows as a would-change.
			fmt.Printf("would upload archive %v\n", name)
			continue
		} else {
			// doesn't exist, upload
			fmt.Printf("uploading archive %v\n", name)
			// ar.URL is actually a local filename at this stage.
			// Unscoped ("" namespace): spec archives are de-duplicated by checksum
			// and may be shared by packages across namespaces, so they cannot be
			// pinned to a single tenant. They upload as legacy (unscoped) ids,
			// readable by any tenant (grandfathered) — scoping the spec path is a
			// tracked follow-up that needs per-package-namespace upload handling.
			uploadedAr, err := pkgutil.UploadArchiveFile(input.Context(), fclient, ar.URL, "")
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
					return fmt.Errorf("unknown archive name %v", strings.TrimPrefix(ar.URL, ARCHIVE_URL_PREFIX))
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

// applyResources applies the given set of fission resources. When dryRun is set
// it performs the read-only diff only, making no changes to the cluster.
func applyResources(input cli.Input, fclient cmd.Client, specDir string, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, map[string]ResourceApplyStatus, error) {

	applyStatus := make(map[string]ResourceApplyStatus)

	// upload archives that need to be uploaded. Changes archive references in fr.Packages.
	err := applyArchives(input, fclient, specDir, fr, dryRun)
	if err != nil {
		return nil, nil, err
	}

	_, ras, err := applyEnvironments(input.Context(), fclient, fr, delete, specAllowConflicts, dryRun)
	if err != nil {
		return nil, nil, fmt.Errorf("environment apply failed: %w", err)
	}
	applyStatus["environment"] = *ras

	pkgMeta, ras, err := applyPackages(input.Context(), fclient, fr, delete, specAllowConflicts, dryRun)
	if err != nil {
		return nil, nil, fmt.Errorf("package apply failed: %w", err)
	}
	applyStatus["package"] = *ras

	// Each reference to a package from a function must contain the resource version
	// of the package. This ensures that various caches can invalidate themselves
	// when the package changes.
	//
	// Under --dry-run pkgMeta carries the package's current ResourceVersion for
	// no-op/created packages, and the dryRunResourceVersion sentinel for packages
	// that would be updated — so a would-be package update correctly cascades into
	// a would-be update of the functions that reference it, matching a real apply.
	for i, f := range fr.Functions {
		if f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypeContainer {
			continue
		}
		k := k8sCache.MetaObjectToName(&metav1.ObjectMeta{
			Namespace: f.Spec.Package.PackageRef.Namespace,
			Name:      f.Spec.Package.PackageRef.Name,
		}).String()
		m, ok := pkgMeta[k]
		if !ok {
			// the function references a package that doesn't exist in the
			// spec. It may exist outside the spec, but we're going to treat
			// that as an error, so that we encourage self-contained specs.
			// Is there a good use case for non-self contained specs?
			return nil, nil, fmt.Errorf("function %v/%v references package %v/%v, which doesn't exist in the specs",
				f.Namespace, f.Name, f.Spec.Package.PackageRef.Namespace, f.Spec.Package.PackageRef.Name)
		}
		fr.Functions[i].Spec.Package.PackageRef.ResourceVersion = m.ResourceVersion
	}

	_, ras, err = applyFunctions(input.Context(), fclient, fr, delete, specAllowConflicts, dryRun)
	if err != nil {
		return nil, nil, fmt.Errorf("function apply failed: %w", err)
	}
	applyStatus["function"] = *ras

	_, ras, err = applyHTTPTriggers(input.Context(), fclient, fr, delete, specAllowConflicts, dryRun)
	if err != nil {
		return nil, nil, fmt.Errorf("HTTPTrigger apply failed: %w", err)
	}
	applyStatus["HTTPTrigger"] = *ras

	_, ras, err = applyKubernetesWatchTriggers(input.Context(), fclient, fr, delete, specAllowConflicts, dryRun)
	if err != nil {
		return nil, nil, fmt.Errorf("kubernetesWatchTrigger apply failed: %w", err)
	}
	applyStatus["KubernetesWatchTrigger"] = *ras

	_, ras, err = applyTimeTriggers(input.Context(), fclient, fr, delete, specAllowConflicts, dryRun)
	if err != nil {
		return nil, nil, fmt.Errorf("timeTrigger apply failed: %w", err)
	}
	applyStatus["TimeTrigger"] = *ras

	_, ras, err = applyWorkflows(input.Context(), fclient, fr, delete, specAllowConflicts, dryRun)
	if err != nil {
		return nil, nil, fmt.Errorf("workflow apply failed: %w", err)
	}
	applyStatus["Workflow"] = *ras

	_, ras, err = applyMessageQueueTriggers(input.Context(), fclient, fr, delete, specAllowConflicts, dryRun)
	if err != nil {
		return nil, nil, fmt.Errorf("messageQueueTrigger apply failed: %w", err)
	}
	applyStatus["MessageQueueTrigger"] = *ras

	return pkgMeta, applyStatus, nil
}

// localArchiveFromSpec creates an archive on the local filesystem from the given spec,
// and returns its path and checksum.
func localArchiveFromSpec(ctx context.Context, specDir string, aus *spectypes.ArchiveUploadSpec) (*fv1.Archive, error) {
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

	// checking if file is a zip
	if match, _ := utils.IsZip(ctx, aus.IncludeGlobs[0]); match && len(aus.IncludeGlobs) == 1 {
		files = append(files, aus.IncludeGlobs[0])
	} else {
		for _, relativeGlob := range aus.IncludeGlobs {
			absGlob := filepath.Join(rootDir, relativeGlob)
			console.Verbose(2, "try to find globs in path '%v'", absGlob)
			fs, err := utils.FindAllGlobs(absGlob)
			if err != nil {
				return nil, fmt.Errorf("invalid glob in archive %v: %v: %w", aus.Name, relativeGlob, err)
			}
			files = append(files, fs...)
		}
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("archive '%v' is empty", aus.Name)
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

		_, err = utils.MakeZipArchiveWithGlobs(ctx, archiveFileName, files...)
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
			return nil, fmt.Errorf("failed to calculate archive checksum for %v (%v): %v", aus.Name, archiveFileName, err)
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

func waitForPackageBuild(ctx context.Context, fclient cmd.Client, pkg *fv1.Package) (*fv1.Package, error) {
	start := time.Now()
	for {
		if pkg.Status.BuildStatus != fv1.BuildStatusRunning {
			return pkg, nil
		}
		if time.Since(start) > 5*time.Minute {
			return nil, fmt.Errorf("package %v has been building for a while, giving up on waiting for it", pkg.Name)
		}

		// TODO watch instead
		time.Sleep(time.Second)

		var err error
		pkg, err = fclient.FissionClientSet.CoreV1().Packages(pkg.ObjectMeta.Namespace).Get(ctx, pkg.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
	}
}

func applyPackages(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	packages := func(ns string) typedv1.PackageInterface {
		return fclient.FissionClientSet.CoreV1().Packages(ns)
	}
	return applyResourceType(ctx, fr, resourceOps[fv1.Package, *fv1.Package]{
		items: func(fr *FissionResources) []fv1.Package { return fr.Packages },
		list: func(ctx context.Context) ([]fv1.Package, error) {
			l, err := packages(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return l.Items, nil
		},
		meta: func(p *fv1.Package) *metav1.ObjectMeta { return &p.ObjectMeta },
		// A package is up to date when its spec matches (or its env + non-empty
		// source + build command match), its metadata matches, and its last
		// build succeeded — otherwise we (re)apply to (re)build it.
		equal: func(existing, desired *fv1.Package) bool {
			specMatches := reflect.DeepEqual(existing.Spec, desired.Spec) ||
				(reflect.DeepEqual(existing.Spec.Environment, desired.Spec.Environment) &&
					!reflect.DeepEqual(existing.Spec.Source, fv1.Archive{}) &&
					reflect.DeepEqual(existing.Spec.Source, desired.Spec.Source) &&
					existing.Spec.BuildCommand == desired.Spec.BuildCommand)
			// "none" (a deploy-archive package needing no build) is as ready a
			// terminal state as "succeeded"; treating only the latter as ready
			// would re-apply unchanged deploy packages on every run.
			ready := existing.Status.BuildStatus == fv1.BuildStatusSucceeded ||
				existing.Status.BuildStatus == fv1.BuildStatusNone
			// A source-build package that has "succeeded" but no deployment
			// archive is in a broken state (the deploy URL was wiped by a
			// previous spec apply). Force a re-apply so the update path can
			// retrigger the build.
			if ready && existing.Spec.BuildCommand != "" &&
				!existing.Spec.Source.IsEmpty() && existing.Spec.Deployment.IsEmpty() {
				ready = false
			}
			return specMatches &&
				isObjectMetaEqual(existing.ObjectMeta, desired.ObjectMeta) &&
				ready
		},
		create: func(ctx context.Context, p *fv1.Package) (*metav1.ObjectMeta, error) {
			n, err := packages(p.Namespace).Create(ctx, p, metav1.CreateOptions{})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		update: func(ctx context.Context, existing, desired *fv1.Package) (*metav1.ObjectMeta, error) {
			// We may be racing the package builder (a previous version might be
			// building), so wait for a non-running build status first. Decide
			// from the live object (existing): desired comes from the spec file
			// and carries no status, so the wait/re-trigger must read the real
			// BuildStatus.
			current, err := waitForPackageBuild(ctx, fclient, existing)
			if err != nil {
				console.Warn(fmt.Sprintf("Error waiting for package '%v' build, ignoring", desired.Name))
				current = existing
			}

			// Determine whether a build must be (re)triggered after the spec
			// Update below. With the /status subresource the main-resource
			// Update cannot touch BuildStatus, so we must issue a separate
			// UpdateStatus to set it back to "pending".
			//
			// A retrigger is needed when:
			//  1. The previous build failed (existing behaviour), OR
			//  2. This is a source-build package (has a build command and
			//     source archive). The spec Update overwrites spec.Deployment
			//     with the empty value from the spec file, wiping the deploy
			//     archive URL that the buildermgr wrote on the last successful
			//     build. Without a retrigger the package would be stuck with
			//     buildstatus=succeeded but no deploy archive.
			retrigger := current.Status.BuildStatus == fv1.BuildStatusFailed ||
				(desired.Spec.BuildCommand != "" && !desired.Spec.Source.IsEmpty())

			// Apply the spec, re-getting on conflict: the buildermgr writes a
			// package's build status concurrently, which can bump the
			// ResourceVersion between our read and this Update.
			n, err := util.UpdateOnConflict(ctx, packages(desired.Namespace), desired.Name, func(cur *fv1.Package) {
				desired.ResourceVersion = cur.ResourceVersion
				*cur = *desired
			})
			if err != nil {
				return nil, err
			}
			// Re-trigger a build via the /status subresource. This is
			// separate from the spec Update above because the apiserver
			// ignores status fields on a main-resource write when the
			// /status subresource is enabled.
			if retrigger {
				if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
					live, gerr := packages(desired.Namespace).Get(ctx, desired.Name, metav1.GetOptions{})
					if gerr != nil {
						return gerr
					}
					live.Status.BuildStatus = fv1.BuildStatusPending
					var uerr error
					n, uerr = packages(desired.Namespace).UpdateStatus(ctx, live, metav1.UpdateOptions{})
					return uerr
				}); err != nil {
					return nil, err
				}
			}
			return &n.ObjectMeta, nil
		},
		delete: func(ctx context.Context, ns, name string) error {
			return packages(ns).Delete(ctx, name, metav1.DeleteOptions{})
		},
	}, delete, specAllowConflicts, dryRun)
}

func applyFunctions(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	functions := func(ns string) typedv1.FunctionInterface {
		return fclient.FissionClientSet.CoreV1().Functions(ns)
	}
	return applyResourceType(ctx, fr, resourceOps[fv1.Function, *fv1.Function]{
		items: func(fr *FissionResources) []fv1.Function { return fr.Functions },
		list: func(ctx context.Context) ([]fv1.Function, error) {
			l, err := functions(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return l.Items, nil
		},
		meta: func(f *fv1.Function) *metav1.ObjectMeta { return &f.ObjectMeta },
		equal: func(e, d *fv1.Function) bool {
			return isObjectMetaEqual(e.ObjectMeta, d.ObjectMeta) && reflect.DeepEqual(e.Spec, d.Spec)
		},
		create: func(ctx context.Context, f *fv1.Function) (*metav1.ObjectMeta, error) {
			n, err := functions(f.Namespace).Create(ctx, f, metav1.CreateOptions{})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		update: func(ctx context.Context, _, d *fv1.Function) (*metav1.ObjectMeta, error) {
			n, err := util.UpdateOnConflict(ctx, functions(d.Namespace), d.Name, func(cur *fv1.Function) {
				d.ResourceVersion = cur.ResourceVersion
				*cur = *d
			})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		delete: func(ctx context.Context, ns, name string) error {
			return functions(ns).Delete(ctx, name, metav1.DeleteOptions{})
		},
	}, delete, specAllowConflicts, dryRun)
}

func applyEnvironments(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	environments := func(ns string) typedv1.EnvironmentInterface {
		return fclient.FissionClientSet.CoreV1().Environments(ns)
	}
	return applyResourceType(ctx, fr, resourceOps[fv1.Environment, *fv1.Environment]{
		items: func(fr *FissionResources) []fv1.Environment { return fr.Environments },
		list: func(ctx context.Context) ([]fv1.Environment, error) {
			l, err := environments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return l.Items, nil
		},
		meta: func(e *fv1.Environment) *metav1.ObjectMeta { return &e.ObjectMeta },
		equal: func(e, d *fv1.Environment) bool {
			return isObjectMetaEqual(e.ObjectMeta, d.ObjectMeta) && reflect.DeepEqual(e.Spec, d.Spec)
		},
		create: func(ctx context.Context, e *fv1.Environment) (*metav1.ObjectMeta, error) {
			n, err := environments(e.Namespace).Create(ctx, e, metav1.CreateOptions{})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		update: func(ctx context.Context, _, d *fv1.Environment) (*metav1.ObjectMeta, error) {
			n, err := util.UpdateOnConflict(ctx, environments(d.Namespace), d.Name, func(cur *fv1.Environment) {
				d.ResourceVersion = cur.ResourceVersion
				*cur = *d
			})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		delete: func(ctx context.Context, ns, name string) error {
			return environments(ns).Delete(ctx, name, metav1.DeleteOptions{})
		},
	}, delete, specAllowConflicts, dryRun)
}

func applyHTTPTriggers(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	triggers := func(ns string) typedv1.HTTPTriggerInterface {
		return fclient.FissionClientSet.CoreV1().HTTPTriggers(ns)
	}
	return applyResourceType(ctx, fr, resourceOps[fv1.HTTPTrigger, *fv1.HTTPTrigger]{
		items: func(fr *FissionResources) []fv1.HTTPTrigger { return fr.HttpTriggers },
		list: func(ctx context.Context) ([]fv1.HTTPTrigger, error) {
			l, err := triggers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return l.Items, nil
		},
		meta: func(t *fv1.HTTPTrigger) *metav1.ObjectMeta { return &t.ObjectMeta },
		equal: func(e, d *fv1.HTTPTrigger) bool {
			return isObjectMetaEqual(e.ObjectMeta, d.ObjectMeta) && reflect.DeepEqual(e.Spec, d.Spec)
		},
		// read-only duplicate-route check; runs in dry-run too so a preview
		// surfaces the conflict a real apply would reject.
		validate: func(ctx context.Context, t *fv1.HTTPTrigger) error {
			return util.CheckHTTPTriggerDuplicates(ctx, fclient, t)
		},
		create: func(ctx context.Context, t *fv1.HTTPTrigger) (*metav1.ObjectMeta, error) {
			n, err := triggers(t.Namespace).Create(ctx, t, metav1.CreateOptions{})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		update: func(ctx context.Context, _, d *fv1.HTTPTrigger) (*metav1.ObjectMeta, error) {
			n, err := util.UpdateOnConflict(ctx, triggers(d.Namespace), d.Name, func(cur *fv1.HTTPTrigger) {
				d.ResourceVersion = cur.ResourceVersion
				*cur = *d
			})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		delete: func(ctx context.Context, ns, name string) error {
			return triggers(ns).Delete(ctx, name, metav1.DeleteOptions{})
		},
	}, delete, specAllowConflicts, dryRun)
}

func applyKubernetesWatchTriggers(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	watches := func(ns string) typedv1.KubernetesWatchTriggerInterface {
		return fclient.FissionClientSet.CoreV1().KubernetesWatchTriggers(ns)
	}
	return applyResourceType(ctx, fr, resourceOps[fv1.KubernetesWatchTrigger, *fv1.KubernetesWatchTrigger]{
		items: func(fr *FissionResources) []fv1.KubernetesWatchTrigger { return fr.KubernetesWatchTriggers },
		list: func(ctx context.Context) ([]fv1.KubernetesWatchTrigger, error) {
			l, err := watches(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return l.Items, nil
		},
		meta: func(t *fv1.KubernetesWatchTrigger) *metav1.ObjectMeta { return &t.ObjectMeta },
		equal: func(e, d *fv1.KubernetesWatchTrigger) bool {
			return isObjectMetaEqual(e.ObjectMeta, d.ObjectMeta) && reflect.DeepEqual(e.Spec, d.Spec)
		},
		create: func(ctx context.Context, t *fv1.KubernetesWatchTrigger) (*metav1.ObjectMeta, error) {
			n, err := watches(t.Namespace).Create(ctx, t, metav1.CreateOptions{})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		update: func(ctx context.Context, _, d *fv1.KubernetesWatchTrigger) (*metav1.ObjectMeta, error) {
			n, err := util.UpdateOnConflict(ctx, watches(d.Namespace), d.Name, func(cur *fv1.KubernetesWatchTrigger) {
				d.ResourceVersion = cur.ResourceVersion
				*cur = *d
			})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		delete: func(ctx context.Context, ns, name string) error {
			return watches(ns).Delete(ctx, name, metav1.DeleteOptions{})
		},
	}, delete, specAllowConflicts, dryRun)
}

func applyTimeTriggers(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	triggers := func(ns string) typedv1.TimeTriggerInterface {
		return fclient.FissionClientSet.CoreV1().TimeTriggers(ns)
	}
	return applyResourceType(ctx, fr, resourceOps[fv1.TimeTrigger, *fv1.TimeTrigger]{
		items: func(fr *FissionResources) []fv1.TimeTrigger { return fr.TimeTriggers },
		list: func(ctx context.Context) ([]fv1.TimeTrigger, error) {
			l, err := triggers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return l.Items, nil
		},
		meta: func(t *fv1.TimeTrigger) *metav1.ObjectMeta { return &t.ObjectMeta },
		equal: func(e, d *fv1.TimeTrigger) bool {
			return isObjectMetaEqual(e.ObjectMeta, d.ObjectMeta) && reflect.DeepEqual(e.Spec, d.Spec)
		},
		create: func(ctx context.Context, t *fv1.TimeTrigger) (*metav1.ObjectMeta, error) {
			n, err := triggers(t.Namespace).Create(ctx, t, metav1.CreateOptions{})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		update: func(ctx context.Context, _, d *fv1.TimeTrigger) (*metav1.ObjectMeta, error) {
			n, err := util.UpdateOnConflict(ctx, triggers(d.Namespace), d.Name, func(cur *fv1.TimeTrigger) {
				d.ResourceVersion = cur.ResourceVersion
				*cur = *d
			})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		delete: func(ctx context.Context, ns, name string) error {
			return triggers(ns).Delete(ctx, name, metav1.DeleteOptions{})
		},
	}, delete, specAllowConflicts, dryRun)
}

func applyWorkflows(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	workflows := func(ns string) typedv1.WorkflowInterface {
		return fclient.FissionClientSet.CoreV1().Workflows(ns)
	}
	return applyResourceType(ctx, fr, resourceOps[fv1.Workflow, *fv1.Workflow]{
		items: func(fr *FissionResources) []fv1.Workflow { return fr.Workflows },
		list: func(ctx context.Context) ([]fv1.Workflow, error) {
			l, err := workflows(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return l.Items, nil
		},
		meta: func(w *fv1.Workflow) *metav1.ObjectMeta { return &w.ObjectMeta },
		equal: func(e, d *fv1.Workflow) bool {
			return isObjectMetaEqual(e.ObjectMeta, d.ObjectMeta) && reflect.DeepEqual(e.Spec, d.Spec)
		},
		create: func(ctx context.Context, w *fv1.Workflow) (*metav1.ObjectMeta, error) {
			n, err := workflows(w.Namespace).Create(ctx, w, metav1.CreateOptions{})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		update: func(ctx context.Context, _, d *fv1.Workflow) (*metav1.ObjectMeta, error) {
			n, err := util.UpdateOnConflict(ctx, workflows(d.Namespace), d.Name, func(cur *fv1.Workflow) {
				d.ResourceVersion = cur.ResourceVersion
				*cur = *d
			})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		delete: func(ctx context.Context, ns, name string) error {
			return workflows(ns).Delete(ctx, name, metav1.DeleteOptions{})
		},
	}, delete, specAllowConflicts, dryRun)
}

func applyMessageQueueTriggers(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	triggers := func(ns string) typedv1.MessageQueueTriggerInterface {
		return fclient.FissionClientSet.CoreV1().MessageQueueTriggers(ns)
	}
	return applyResourceType(ctx, fr, resourceOps[fv1.MessageQueueTrigger, *fv1.MessageQueueTrigger]{
		items: func(fr *FissionResources) []fv1.MessageQueueTrigger { return fr.MessageQueueTriggers },
		list: func(ctx context.Context) ([]fv1.MessageQueueTrigger, error) {
			l, err := triggers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return l.Items, nil
		},
		meta: func(t *fv1.MessageQueueTrigger) *metav1.ObjectMeta { return &t.ObjectMeta },
		equal: func(e, d *fv1.MessageQueueTrigger) bool {
			return isObjectMetaEqual(e.ObjectMeta, d.ObjectMeta) && reflect.DeepEqual(e.Spec, d.Spec)
		},
		create: func(ctx context.Context, t *fv1.MessageQueueTrigger) (*metav1.ObjectMeta, error) {
			n, err := triggers(t.Namespace).Create(ctx, t, metav1.CreateOptions{})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		update: func(ctx context.Context, _, d *fv1.MessageQueueTrigger) (*metav1.ObjectMeta, error) {
			n, err := util.UpdateOnConflict(ctx, triggers(d.Namespace), d.Name, func(cur *fv1.MessageQueueTrigger) {
				d.ResourceVersion = cur.ResourceVersion
				*cur = *d
			})
			if err != nil {
				return nil, err
			}
			return &n.ObjectMeta, nil
		},
		delete: func(ctx context.Context, ns, name string) error {
			return triggers(ns).Delete(ctx, name, metav1.DeleteOptions{})
		},
	}, delete, specAllowConflicts, dryRun)
}

func isObjectMetaEqual(existingObj, newObj metav1.ObjectMeta) bool {
	return reflect.DeepEqual(existingObj.Labels, newObj.Labels) && reflect.DeepEqual(existingObj.Annotations, newObj.Annotations)
}
