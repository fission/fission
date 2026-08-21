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
	for i := range fr.FunctionAliases {
		if fr.FunctionAliases[i].Namespace == "" || input.Bool(flagkey.ForceNamespace) {
			fr.FunctionAliases[i].Namespace = currentNS
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

// listAllPackages enumerates every Package in every namespace.
//
// This is the single cluster-wide Package enumeration applyResources performs:
// the resulting snapshot is threaded to both consumers that need it (archive
// de-duplication in applyArchives and the create/update/delete diff in
// applyPackages) rather than each listing for itself. See applyResources for
// what sharing one snapshot across both costs.
//
// It is not the only Package listing a `spec apply` command makes: validation
// lists them too (getAllPackages, in validate.go), as does the build watch
// under --wait.
func listAllPackages(ctx context.Context, fclient cmd.Client) ([]fv1.Package, error) {
	l, err := fclient.FissionClientSet.CoreV1().Packages(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return l.Items, nil
}

// applyArchives figures out the set of archives that need to be uploaded, and uploads them.
// Under dryRun the read-only work still runs — local archives are built/checksummed
// and matched against archives already on the cluster — so the resolved Package
// specs (and therefore the diff) are accurate for unchanged archives; only the
// actual upload of a new/changed archive is skipped (such a Package legitimately
// shows as a would-create/update).
//
// livePkgs is the caller's cluster-wide Package snapshot; it is read only to
// build the content-index of archives already present on the cluster.
func applyArchives(input cli.Input, fclient cmd.Client, specDir string, fr *FissionResources, livePkgs []fv1.Package, dryRun bool) error {
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

	// make content-indexed map of available archives from the caller's snapshot
	availableArchives := make(map[string]string) // (sha256 -> url)
	for _, pkg := range livePkgs {
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

	// One cluster-wide Package enumeration per apply, shared by the two consumers
	// that need it: applyArchives indexes it by archive checksum to skip
	// re-uploading bytes already on the cluster, and applyPackages diffs the spec
	// against it.
	//
	// Nothing the CLI itself does between the two consumers writes a Package:
	// pkgutil.UploadArchiveFile only pushes archive bytes to storagesvc, and
	// applyEnvironments touches only Environments.
	//
	// The buildermgr, though, is a genuine concurrent writer of these same
	// objects — updatePackage sets spec.Deployment, and the status write that
	// follows it sets status.BuildStatus — and those are exactly the fields the
	// equal() closure in applyPackages reads. So sharing one snapshot does not
	// merely move the staleness window, it widens it: the second List this
	// replaces ran *after* the archive uploads, this one runs before them, so
	// the diff can now be reading a package set that is stale by the whole
	// upload duration.
	//
	// That is accepted deliberately. A build landing inside the window makes its
	// package read not-ready, so it takes the update path and gets retriggered:
	// redundant work that converges on the next reconcile, not lost data. The
	// alternative is the second full cluster-wide enumeration on every apply
	// that issue #3664 was filed to remove.
	livePkgs, err := listAllPackages(input.Context(), fclient)
	if err != nil {
		return nil, nil, fmt.Errorf("list packages: %w", err)
	}

	// upload archives that need to be uploaded. Changes archive references in fr.Packages.
	err = applyArchives(input, fclient, specDir, fr, livePkgs, dryRun)
	if err != nil {
		return nil, nil, err
	}

	_, ras, err := applyEnvironments(input.Context(), fclient, fr, delete, specAllowConflicts, dryRun)
	if err != nil {
		return nil, nil, fmt.Errorf("environment apply failed: %w", err)
	}
	applyStatus["environment"] = *ras

	pkgMeta, ras, err := applyPackages(input.Context(), fclient, fr, livePkgs, delete, specAllowConflicts, dryRun)
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
	// Packages this apply actually wrote. Copying a package's CURRENT
	// ResourceVersion into every referencing function is only correct for
	// these: a package's ResourceVersion also drifts on controller STATUS
	// writes (buildermgr records a content hash), so stamping unconditionally
	// re-stamps functions whose package nobody touched — which bumps their
	// generation, recycles their pods and mints an RFC-0025 version. A GitOps
	// controller reapplies on every sync, so that is continuous churn, not a
	// one-off. See TestSpecApplyIsIdempotent.
	written := make(map[string]bool, len(ras.Created)+len(ras.Updated))
	for _, m := range ras.Created {
		written[k8sCache.MetaObjectToName(m).String()] = true
	}
	for _, m := range ras.Updated {
		written[k8sCache.MetaObjectToName(m).String()] = true
	}

	// Stamps the live functions already carry, so an untouched package leaves
	// its referencing functions byte-identical instead of empty (which would
	// itself read as a change and force an update).
	liveStamps, err := liveFunctionStamps(input.Context(), fclient)
	if err != nil {
		return nil, nil, err
	}

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

		// Default to the package's current ResourceVersion. That is right when
		// this apply wrote the package (the change must reach running pods),
		// and it is the only stamp available for a function that is new, or
		// that this spec repoints at a different package.
		rv := m.ResourceVersion

		// The one case that must NOT take the current version: the package was
		// untouched by this apply and the live function already references that
		// same package. Re-stamping there would rewrite an unchanged function
		// on every apply, minting spurious RFC-0025 versions.
		//
		// "Same package" is load-bearing. A spec that repoints a function from
		// pkgA to an untouched pkgB must not inherit pkgA's stamp: the
		// reference would name pkgB with pkgA's ResourceVersion, the next apply
		// would preserve that same wrong value, and nothing converges —
		// package restamping only fires when pkgB itself is written.
		fnKey := k8sCache.MetaObjectToName(&f.ObjectMeta).String()
		if live, isLive := liveStamps[fnKey]; !written[k] && isLive &&
			live.Name == f.Spec.Package.PackageRef.Name &&
			live.Namespace == f.Spec.Package.PackageRef.Namespace {
			rv = live.ResourceVersion
		}
		fr.Functions[i].Spec.Package.PackageRef.ResourceVersion = rv
	}

	_, ras, err = applyFunctions(input.Context(), fclient, fr, delete, specAllowConflicts, dryRun)
	if err != nil {
		return nil, nil, fmt.Errorf("function apply failed: %w", err)
	}
	applyStatus["function"] = *ras

	// Aliases apply after functions: they carry an ownerRef to the Function
	// they target (set when the Function already exists on the cluster — see
	// applyFunctionAliases) and, for name-pinned aliases (spec.Version), the
	// webhook resolves against existing FunctionVersions, which only the
	// version-control loop / `fission fn publish` create (FunctionVersion is
	// deliberately not spec-managed). A name-pinned alias whose target version
	// doesn't exist yet is rejected by the webhook; that error surfaces as-is
	// so the user knows to publish first. A digest-pinned alias
	// (spec.PackageDigest) has no such precondition — it resolves
	// asynchronously once a matching version is published (eventual
	// consistency, RFC-0025).
	_, ras, err = applyFunctionAliases(input.Context(), fclient, fr, delete, specAllowConflicts, dryRun)
	if err != nil {
		return nil, nil, fmt.Errorf("functionAlias apply failed: %w", err)
	}
	applyStatus["FunctionAlias"] = *ras

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

// applyPackages reconciles the spec's Packages against livePkgs, the caller's
// cluster-wide Package snapshot. The snapshot is supplied rather than fetched
// here so that applyResources performs one Package enumeration rather than two;
// callers with no snapshot in hand (spec destroy) obtain one from
// listAllPackages. See applyResources for how stale the snapshot can be by the
// time the diff below reads it.
func applyPackages(ctx context.Context, fclient cmd.Client, fr *FissionResources, livePkgs []fv1.Package, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	packages := fclient.FissionClientSet.CoreV1().Packages
	// A package is up to date when its spec matches (or its env + non-empty
	// source + build command match), its metadata matches, and its last
	// build succeeded — otherwise we (re)apply to (re)build it.
	equal := func(existing, desired *fv1.Package) bool {
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
	}
	ops := standardOps(
		packages,
		func(fr *FissionResources) []fv1.Package { return fr.Packages },
		func(l *fv1.PackageList) []fv1.Package { return l.Items },
		func(o *fv1.Package) *metav1.ObjectMeta { return &o.ObjectMeta },
		equal,
	)
	// The snapshot is already in hand, so this needs no context.
	ops.list = func(context.Context) ([]fv1.Package, error) { return livePkgs, nil }
	ops.update = func(ctx context.Context, existing, desired *fv1.Package) (*metav1.ObjectMeta, error) {
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
	}
	return applyResourceType(ctx, fr, ops, delete, specAllowConflicts, dryRun)
}

// liveFunctionStamps returns each existing function's current
// PackageRef.ResourceVersion, keyed by namespace/name.
//
// Needed because a function in a spec FILE carries no ResourceVersion — that
// field is a runtime stamp, not something a user writes. So "leave it alone"
// cannot mean "leave it empty": an empty stamp differs from what is live and
// would force the very update this is avoiding.
// The whole PackageRef is returned, not just the stamp: a stamp is only worth
// preserving when the live function still points at the SAME package the spec
// now names. Keying on the function alone would carry the old package's
// ResourceVersion onto a repointed reference.
func liveFunctionStamps(ctx context.Context, fclient cmd.Client) (map[string]fv1.PackageRef, error) {
	list, err := fclient.FissionClientSet.CoreV1().Functions(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list functions to preserve package stamps: %w", err)
	}
	out := make(map[string]fv1.PackageRef, len(list.Items))
	for i := range list.Items {
		fn := &list.Items[i]
		out[k8sCache.MetaObjectToName(&fn.ObjectMeta).String()] = fn.Spec.Package.PackageRef
	}
	return out, nil
}

func applyFunctions(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	return applyResourceType(ctx, fr, standardOps(
		fclient.FissionClientSet.CoreV1().Functions,
		func(fr *FissionResources) []fv1.Function { return fr.Functions },
		func(l *fv1.FunctionList) []fv1.Function { return l.Items },
		func(o *fv1.Function) *metav1.ObjectMeta { return &o.ObjectMeta },
		func(e, d *fv1.Function) bool {
			return isObjectMetaEqual(e.ObjectMeta, d.ObjectMeta) && reflect.DeepEqual(e.Spec, d.Spec)
		},
	), delete, specAllowConflicts, dryRun)
}

func applyEnvironments(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	return applyResourceType(ctx, fr, standardOps(
		fclient.FissionClientSet.CoreV1().Environments,
		func(fr *FissionResources) []fv1.Environment { return fr.Environments },
		func(l *fv1.EnvironmentList) []fv1.Environment { return l.Items },
		func(o *fv1.Environment) *metav1.ObjectMeta { return &o.ObjectMeta },
		func(e, d *fv1.Environment) bool {
			return isObjectMetaEqual(e.ObjectMeta, d.ObjectMeta) && reflect.DeepEqual(e.Spec, d.Spec)
		},
	), delete, specAllowConflicts, dryRun)
}

func applyHTTPTriggers(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	ops := standardOps(
		fclient.FissionClientSet.CoreV1().HTTPTriggers,
		func(fr *FissionResources) []fv1.HTTPTrigger { return fr.HttpTriggers },
		func(l *fv1.HTTPTriggerList) []fv1.HTTPTrigger { return l.Items },
		func(o *fv1.HTTPTrigger) *metav1.ObjectMeta { return &o.ObjectMeta },
		func(e, d *fv1.HTTPTrigger) bool {
			return isObjectMetaEqual(e.ObjectMeta, d.ObjectMeta) && reflect.DeepEqual(e.Spec, d.Spec)
		},
	)
	// read-only duplicate-route check; runs in dry-run too so a preview
	// surfaces the conflict a real apply would reject.
	ops.validate = func(ctx context.Context, t *fv1.HTTPTrigger) error {
		return util.CheckHTTPTriggerDuplicates(ctx, fclient, t)
	}
	return applyResourceType(ctx, fr, ops, delete, specAllowConflicts, dryRun)
}

func applyKubernetesWatchTriggers(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	return applyResourceType(ctx, fr, standardOps(
		fclient.FissionClientSet.CoreV1().KubernetesWatchTriggers,
		func(fr *FissionResources) []fv1.KubernetesWatchTrigger { return fr.KubernetesWatchTriggers },
		func(l *fv1.KubernetesWatchTriggerList) []fv1.KubernetesWatchTrigger { return l.Items },
		func(o *fv1.KubernetesWatchTrigger) *metav1.ObjectMeta { return &o.ObjectMeta },
		func(e, d *fv1.KubernetesWatchTrigger) bool {
			return isObjectMetaEqual(e.ObjectMeta, d.ObjectMeta) && reflect.DeepEqual(e.Spec, d.Spec)
		},
	), delete, specAllowConflicts, dryRun)
}

func applyTimeTriggers(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	return applyResourceType(ctx, fr, standardOps(
		fclient.FissionClientSet.CoreV1().TimeTriggers,
		func(fr *FissionResources) []fv1.TimeTrigger { return fr.TimeTriggers },
		func(l *fv1.TimeTriggerList) []fv1.TimeTrigger { return l.Items },
		func(o *fv1.TimeTrigger) *metav1.ObjectMeta { return &o.ObjectMeta },
		func(e, d *fv1.TimeTrigger) bool {
			return isObjectMetaEqual(e.ObjectMeta, d.ObjectMeta) && reflect.DeepEqual(e.Spec, d.Spec)
		},
	), delete, specAllowConflicts, dryRun)
}

func applyWorkflows(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	return applyResourceType(ctx, fr, standardOps(
		fclient.FissionClientSet.CoreV1().Workflows,
		func(fr *FissionResources) []fv1.Workflow { return fr.Workflows },
		func(l *fv1.WorkflowList) []fv1.Workflow { return l.Items },
		func(o *fv1.Workflow) *metav1.ObjectMeta { return &o.ObjectMeta },
		func(e, d *fv1.Workflow) bool {
			return isObjectMetaEqual(e.ObjectMeta, d.ObjectMeta) && reflect.DeepEqual(e.Spec, d.Spec)
		},
	), delete, specAllowConflicts, dryRun)
}

func applyMessageQueueTriggers(ctx context.Context, fclient cmd.Client, fr *FissionResources, delete bool, specAllowConflicts bool, dryRun bool) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {
	return applyResourceType(ctx, fr, standardOps(
		fclient.FissionClientSet.CoreV1().MessageQueueTriggers,
		func(fr *FissionResources) []fv1.MessageQueueTrigger { return fr.MessageQueueTriggers },
		func(l *fv1.MessageQueueTriggerList) []fv1.MessageQueueTrigger { return l.Items },
		func(o *fv1.MessageQueueTrigger) *metav1.ObjectMeta { return &o.ObjectMeta },
		func(e, d *fv1.MessageQueueTrigger) bool {
			return isObjectMetaEqual(e.ObjectMeta, d.ObjectMeta) && reflect.DeepEqual(e.Spec, d.Spec)
		},
	), delete, specAllowConflicts, dryRun)
}

func isObjectMetaEqual(existingObj, newObj metav1.ObjectMeta) bool {
	return reflect.DeepEqual(existingObj.Labels, newObj.Labels) && reflect.DeepEqual(existingObj.Annotations, newObj.Annotations)
}
