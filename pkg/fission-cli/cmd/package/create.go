// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package _package

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/dchest/uniuri"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/utils/uuid"
)

type CreateSubCommand struct {
	cmd.CommandActioner
}

func Create(input cli.Input) error {
	return (&CreateSubCommand{}).do(input)
}

func (opts *CreateSubCommand) do(input cli.Input) error {
	err := opts.run(input)
	if err != nil {
		return err
	}
	return nil
}

func (opts *CreateSubCommand) run(input cli.Input) error {
	pkgName := input.String(flagkey.PkgName)
	if len(pkgName) == 0 {
		if input.Bool(flagkey.SpecSave) && len(input.String(flagkey.PkgName)) == 0 {
			return fmt.Errorf("--%v is necessary when creating spec file", flagkey.PkgName)
		} else {
			console.Warn(fmt.Sprintf("--%v will be soon marked as required flag, see 'help' for details", flagkey.HtName))
		}
	}

	envName := input.String(flagkey.PkgEnvironment)

	userProvidedNS, pkgNamespace, err := opts.GetResourceNamespace(input, flagkey.NamespacePackage)
	if err != nil {
		return fv1.AggregateValidationErrors("Package", err)
	}

	srcArchiveFiles := input.StringSlice(flagkey.PkgSrcArchive)
	deployArchiveFiles := input.StringSlice(flagkey.PkgDeployArchive)
	buildcmd := input.String(flagkey.PkgBuildCmd)
	ociImage := input.String(flagkey.PkgOCI)

	noZip := false
	code := input.String(flagkey.PkgCode)
	if len(code) == 0 {
		deployArchiveFiles = input.StringSlice(flagkey.PkgDeployArchive)
	} else {
		deployArchiveFiles = append(deployArchiveFiles, input.String(flagkey.PkgCode))
		noZip = true
	}

	if err := ValidateArchiveSources(code, srcArchiveFiles, deployArchiveFiles, ociImage); err != nil {
		return err
	}

	var specDir, specFile string

	if input.Bool(flagkey.SpecSave) {
		// since package CRD created using --spec, not validate by k8s. So we need to validate it and make sure package name is not more than 63 characters.
		if len(pkgName) > 63 {
			return fmt.Errorf("error creating package: package name %v, must be no more than 63 characters", pkgName)
		}

		specDir = util.GetSpecDir(input)
		specIgnore := util.GetSpecIgnore(input)
		fr, err := spec.ReadSpecs(specDir, specIgnore, false)
		if err != nil {
			return fmt.Errorf("error reading spec in '%v': %w", specDir, err)
		}
		exists, err := fr.ExistsInSpecs(fv1.Environment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      envName,
				Namespace: userProvidedNS,
			},
		})
		if err != nil {
			return err
		}
		if !exists {
			console.Warn(fmt.Sprintf("Package '%s' references unknown Environment '%s' in Namespace '%s', please create it before applying spec",
				pkgName, envName, userProvidedNS))
		}

		specDir = util.GetSpecDir(input)
		specFile = fmt.Sprintf("package-%s.yaml", pkgName)
	}

	_, err = CreatePackage(input, opts.Client(), pkgName, pkgNamespace, envName,
		srcArchiveFiles, deployArchiveFiles, buildcmd, specDir, specFile, noZip, userProvidedNS, ociImage)

	return err
}

// ValidateArchiveSources enforces that --oci is not combined with
// --code/--src/--deploy and that at least one code source is given. It is
// shared by `package create` and `fn create`.
// isClusterLocalRef reports whether an image reference's registry host is a
// cluster-DNS name (resolvable by pods, NOT by the kubelet on nodes).
func isClusterLocalRef(ref string) bool {
	host := ref
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return strings.HasSuffix(host, ".svc") || strings.Contains(host, ".svc.")
}

// splitImageDigest separates a trailing @sha256:... pin from an image
// reference, so users can pass the fully pinned form a registry or CI system
// hands them and the Package records the digest in its own field.
func splitImageDigest(ref string) (image, digest string) {
	if i := strings.LastIndex(ref, "@sha256:"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

func ValidateArchiveSources(code string, srcArchiveFiles, deployArchiveFiles []string, ociImage string) error {
	if len(ociImage) > 0 && (len(code) > 0 || len(srcArchiveFiles) > 0 || len(deployArchiveFiles) > 0) {
		return fmt.Errorf("--%v cannot be combined with --%v, --%v, or --%v", flagkey.PkgOCI, flagkey.PkgCode, flagkey.PkgSrcArchive, flagkey.PkgDeployArchive)
	}
	if len(code) == 0 && len(srcArchiveFiles) == 0 && len(deployArchiveFiles) == 0 && len(ociImage) == 0 {
		return fmt.Errorf("need --%v or --%v or --%v or --%v argument", flagkey.PkgCode, flagkey.PkgSrcArchive, flagkey.PkgDeployArchive, flagkey.PkgOCI)
	}
	return nil
}

// TODO: get all necessary value from CLI input directly
func CreatePackage(input cli.Input, client cmd.Client, pkgName string, pkgNamespace string, envName string,
	srcArchiveFiles []string, deployArchiveFiles []string, buildcmd string, specDir string, specFile string, noZip bool, userProvidedNS string, ociImage string) (*metav1.ObjectMeta, error) {

	insecure := input.Bool(flagkey.PkgInsecure)
	deployChecksum := input.String(flagkey.PkgDeployChecksum)
	srcChecksum := input.String(flagkey.PkgSrcChecksum)

	pkgSpec := fv1.PackageSpec{
		Environment: fv1.EnvironmentReference{
			Namespace: pkgNamespace,
			Name:      envName,
		},
	}
	if input.Bool(flagkey.SpecSave) || input.Bool(flagkey.SpecDry) {
		pkgSpec = fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{
				Namespace: userProvidedNS,
				Name:      envName,
			},
		}
	}

	var pkgStatus fv1.BuildStatus = fv1.BuildStatusSucceeded

	if len(ociImage) > 0 {
		// The OCI archive is built inline: no file globbing, zipping, or
		// upload happens for a pre-built image reference (RFC-0001).
		// ValidateArchiveSources guarantees no overlap with file archives.
		if isClusterLocalRef(ociImage) {
			console.Warn(fmt.Sprintf("%q is a cluster-DNS registry name: nodes cannot resolve it, so image-volume mounts will fail and only fetcher-pull delivery will work. Use a node-resolvable registry address if you want image volumes.", ociImage))
		}
		// Accept a fully pinned reference (repo:tag@sha256:...) and split
		// the digest into its own field — the form CI/CD pipelines paste
		// when promoting one immutable artifact across environments.
		image, digest := splitImageDigest(ociImage)
		pkgSpec.Deployment = fv1.Archive{
			Type: fv1.ArchiveTypeOCI,
			OCI:  &fv1.OCIArchive{Image: image, Digest: digest},
		}
		// Cosmetic: the /status subresource strips client-set status on
		// create; Archive.IsEmpty + buildermgr setInitialBuildStatus is the
		// load-bearing "nothing to build" mechanism.
		pkgStatus = fv1.BuildStatusNone
		if len(pkgName) == 0 {
			pkgName = util.KubifyName(fmt.Sprintf("%v-%v", path.Base(ociImage), uniuri.NewLen(4)))
		}
	}

	if len(deployArchiveFiles) > 0 {
		if len(specFile) > 0 { // we should do this in all cases, i think
			pkgStatus = fv1.BuildStatusNone
		}
		deployment, err := CreateArchive(client, input, deployArchiveFiles, noZip, insecure, deployChecksum, specDir, specFile, pkgNamespace)
		if err != nil {
			return nil, fmt.Errorf("error creating deploy archive: %w", err)
		}
		pkgSpec.Deployment = *deployment
		if len(pkgName) == 0 {
			pkgName = util.KubifyName(fmt.Sprintf("%v-%v", path.Base(deployArchiveFiles[0]), uniuri.NewLen(4)))
		}
	}
	if len(srcArchiveFiles) > 0 {
		source, err := CreateArchive(client, input, srcArchiveFiles, false, insecure, srcChecksum, specDir, specFile, pkgNamespace)
		if err != nil {
			return nil, fmt.Errorf("error creating source archive: %w", err)
		}
		pkgSpec.Source = *source
		pkgStatus = fv1.BuildStatusPending // set package build status to pending
		if len(pkgName) == 0 {
			pkgName = util.KubifyName(fmt.Sprintf("%v-%v", path.Base(srcArchiveFiles[0]), uniuri.NewLen(4)))
		}
	}

	if len(buildcmd) > 0 {
		pkgSpec.BuildCommand = buildcmd
	}

	if len(pkgName) == 0 {
		pkgName = strings.ToLower(uuid.NewString())
	}

	pkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pkgName,
			Namespace: userProvidedNS,
		},
		Spec: pkgSpec,
		Status: fv1.PackageStatus{
			BuildStatus:         pkgStatus,
			LastUpdateTimestamp: metav1.Time{Time: time.Now().UTC()},
		},
	}

	if input.Bool(flagkey.SpecDry) {
		return &pkg.ObjectMeta, spec.SpecDry(*pkg)
	}

	if input.Bool(flagkey.SpecSave) {
		// if a package with the same spec exists, don't create a new spec file
		fr, err := spec.ReadSpecs(util.GetSpecDir(input), util.GetSpecIgnore(input), false)
		if err != nil {
			return nil, fmt.Errorf("error reading specs: %w", err)
		}

		obj := fr.SpecExists(pkg, true, true)
		if obj != nil {
			pkg := obj.(*fv1.Package)
			fmt.Printf("Re-using previously created package %v\n", pkg.Name)
			return &pkg.ObjectMeta, nil
		}

		err = spec.SpecSave(*pkg, specFile, false)
		if err != nil {
			return nil, fmt.Errorf("error saving package spec: %w", err)
		}
		return &pkg.ObjectMeta, nil
	} else {
		pkg.Namespace = pkgNamespace

		pkgMetadata, err := client.FissionClientSet.CoreV1().Packages(pkgNamespace).Create(input.Context(), pkg, metav1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("error creating package: %w", err)
		}
		fmt.Printf("Package '%v' created\n", pkgMetadata.GetName())
		return &pkgMetadata.ObjectMeta, nil
	}
}
