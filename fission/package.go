/*
Copyright 2017 The Fission Authors.

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
	"bytes"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/urfave/cli"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission"
	"github.com/fission/fission/fission/lib"
)

func pkgCreate(c *cli.Context) error {
	client := lib.GetClient(c.GlobalString("server"))

	pkgNamespace := c.String("pkgNamespace")
	envName := c.String("env")
	if len(envName) == 0 {
		return lib.MissingArgError("env")
	}
	envNamespace := c.String("envNamespace")
	srcArchiveName := c.String("src")
	deployArchiveName := c.String("deploy")
	buildcmd := c.String("buildcmd")

	if len(srcArchiveName) == 0 && len(deployArchiveName) == 0 {
		return lib.GeneralError("Missing argument. Need --sourcearchive to specify source archive, or use --deployarchive to specify deployment archive.")
	}
	if len(srcArchiveName) > 0 && len(deployArchiveName) > 0 {
		return lib.GeneralError("Need either of --sourcearchive or --deployarchive and not both arguments.")
	}

	meta, err := lib.CreatePackage(client, pkgNamespace, envName, envNamespace, srcArchiveName, deployArchiveName, buildcmd, "")
	if err != nil {
		return lib.FailedToError(err, "create package")
	}
	fmt.Printf("Package '%v' created\n", meta.GetName())

	return nil
}

func pkgUpdate(c *cli.Context) error {
	client := lib.GetClient(c.GlobalString("server"))

	pkgName := c.String("name")
	if len(pkgName) == 0 {
		return lib.MissingArgError("name")
	}
	pkgNamespace := c.String("pkgNamespace")

	force := c.Bool("f")
	envName := c.String("env")
	envNamespace := c.String("envNamespace")
	srcArchiveName := c.String("src")
	deployArchiveName := c.String("deploy")
	buildcmd := c.String("buildcmd")

	if len(srcArchiveName) > 0 && len(deployArchiveName) > 0 {
		return lib.GeneralError("Need either of --sourcearchive or --deployarchive and not both arguments.")
	}

	if len(srcArchiveName) == 0 && len(deployArchiveName) == 0 &&
		len(envName) == 0 && len(buildcmd) == 0 {
		return lib.GeneralError("Need --env or --sourcearchive or --deployarchive or --buildcmd argument.")
	}

	pkg, err := client.PackageGet(&metav1.ObjectMeta{
		Namespace: pkgNamespace,
		Name:      pkgName,
	})
	if err != nil {
		return lib.FailedToError(err, "get package")
	}

	// if the new env specified is the same as the old one, no need to update package
	// same is true for all update parameters, but, for now, we dont check all of them - because, its ok to
	// re-write the object with same old values, we just end up getting a new resource version for the object.
	if len(envName) > 0 && envName == pkg.Spec.Environment.Name {
		envName = ""
	}

	if envNamespace == pkg.Spec.Environment.Namespace {
		envNamespace = ""
	}

	fnList, err := lib.GetFunctionsByPackage(client, pkg.Metadata.Name, pkg.Metadata.Namespace)
	if err != nil {
		return lib.FailedToError(err, "get function list")
	}

	if !force && len(fnList) > 1 {
		return lib.GeneralError("Package is used by multiple functions, use --force to force update")
	}

	newPkgMeta, err := lib.UpdatePackage(client, pkg,
		envName, envNamespace, srcArchiveName, deployArchiveName, buildcmd, false)
	if err != nil {
		return lib.FailedToError(err, "update package")
	}

	// update resource version of package reference of functions that shared the same package
	for _, fn := range fnList {
		fn.Spec.Package.PackageRef.ResourceVersion = newPkgMeta.ResourceVersion
		_, err := client.FunctionUpdate(&fn)
		if err != nil {
			return lib.FailedToError(err, "update function")
		}
	}

	fmt.Printf("Package '%v' updated\n", newPkgMeta.GetName())

	return nil
}

func pkgSourceGet(c *cli.Context) error {
	client := lib.GetClient(c.GlobalString("server"))

	pkgName := c.String("name")
	if len(pkgName) == 0 {
		return lib.MissingArgError("name")
	}
	pkgNamespace := c.String("pkgNamespace")

	output := c.String("output")

	pkg, err := client.PackageGet(&metav1.ObjectMeta{
		Namespace: pkgNamespace,
		Name:      pkgName,
	})
	if err != nil {
		return err
	}

	var reader io.Reader

	if pkg.Spec.Source.Type == fission.ArchiveTypeLiteral {
		reader = bytes.NewReader(pkg.Spec.Source.Literal)
	} else if pkg.Spec.Source.Type == fission.ArchiveTypeUrl {
		readCloser := lib.DownloadStoragesvcURL(client, pkg.Spec.Source.URL)
		defer readCloser.Close()
		reader = readCloser
	}

	if len(output) > 0 {
		return lib.WriteToFileFromReader(output, reader)
	}
	_, err = io.Copy(os.Stdout, reader)
	return err
}

func pkgDeployGet(c *cli.Context) error {
	client := lib.GetClient(c.GlobalString("server"))

	pkgName := c.String("name")
	if len(pkgName) == 0 {
		return lib.MissingArgError("name")
	}
	pkgNamespace := c.String("pkgNamespace")

	output := c.String("output")

	pkg, err := client.PackageGet(&metav1.ObjectMeta{
		Namespace: pkgNamespace,
		Name:      pkgName,
	})
	if err != nil {
		return err
	}

	var reader io.Reader

	if pkg.Spec.Deployment.Type == fission.ArchiveTypeLiteral {
		reader = bytes.NewReader(pkg.Spec.Deployment.Literal)
	} else if pkg.Spec.Deployment.Type == fission.ArchiveTypeUrl {
		readCloser := lib.DownloadStoragesvcURL(client, pkg.Spec.Deployment.URL)
		defer readCloser.Close()
		reader = readCloser
	}

	if len(output) > 0 {
		return lib.WriteToFileFromReader(output, reader)
	}
	_, err = io.Copy(os.Stdout, reader)
	return err
}

func pkgInfo(c *cli.Context) error {
	client := lib.GetClient(c.GlobalString("server"))

	pkgName := c.String("name")
	if len(pkgName) == 0 {
		return lib.MissingArgError("name")
	}
	pkgNamespace := c.String("pkgNamespace")

	pkg, err := client.PackageGet(&metav1.ObjectMeta{
		Namespace: pkgNamespace,
		Name:      pkgName,
	})
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\n", "Name:", pkg.Metadata.Name)
	fmt.Fprintf(w, "%v\t%v\n", "Environment:", pkg.Spec.Environment.Name)
	fmt.Fprintf(w, "%v\t%v\n", "Status:", pkg.Status.BuildStatus)
	fmt.Fprintf(w, "%v\n%v", "Build Logs:", pkg.Status.BuildLog)
	w.Flush()

	return nil
}

func pkgList(c *cli.Context) error {
	client := lib.GetClient(c.GlobalString("server"))
	// option for the user to list all orphan packages (not referenced by any function)
	listOrphans := c.Bool("orphan")
	pkgNamespace := c.String("pkgNamespace")

	pkgList, err := client.PackageList(pkgNamespace)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\n", "NAME", "BUILD_STATUS", "ENV")
	if listOrphans {
		for _, pkg := range pkgList {
			fnList, err := lib.GetFunctionsByPackage(client, pkg.Metadata.Name, pkg.Metadata.Namespace)
			if err != nil {
				return lib.FailedToError(err, fmt.Sprintf("get functions sharing package %s", pkg.Metadata.Name))
			}
			if len(fnList) == 0 {
				fmt.Fprintf(w, "%v\t%v\t%v\n", pkg.Metadata.Name, pkg.Status.BuildStatus, pkg.Spec.Environment.Name)
			}
		}
	} else {
		for _, pkg := range pkgList {
			fmt.Fprintf(w, "%v\t%v\t%v\n", pkg.Metadata.Name,
				pkg.Status.BuildStatus, pkg.Spec.Environment.Name)
		}
	}

	w.Flush()

	return nil
}

func pkgDelete(c *cli.Context) error {
	client := lib.GetClient(c.GlobalString("server"))

	pkgName := c.String("name")
	pkgNamespace := c.String("pkgNamespace")
	deleteOrphans := c.Bool("orphan")

	if len(pkgName) == 0 && !deleteOrphans {
		fmt.Println("Need --name argument or --orphan flag.")
		return nil
	}
	if len(pkgName) != 0 && deleteOrphans {
		fmt.Println("Need either --name argument or --orphan flag")
		return nil
	}

	if len(pkgName) != 0 {
		force := c.Bool("f")

		_, err := client.PackageGet(&metav1.ObjectMeta{
			Namespace: pkgNamespace,
			Name:      pkgName,
		})
		if err != nil {
			return lib.FailedToError(err, "find package")
		}

		fnList, err := lib.GetFunctionsByPackage(client, pkgName, pkgNamespace)

		if !force && len(fnList) > 0 {
			return lib.GeneralError("Package is used by at least one function, use -f to force delete")
		}

		err = lib.DeletePackage(client, pkgName, pkgNamespace)
		if err != nil {
			return err
		}

		fmt.Printf("Package '%v' deleted\n", pkgName)
	} else {
		err := lib.DeleteOrphanPkgs(client, pkgNamespace)
		if err != nil {
			return lib.FailedToError(err, "error deleting orphan packages")
		}
		fmt.Println("Orphan packages deleted")
	}

	return nil
}

func pkgRebuild(c *cli.Context) error {
	client := lib.GetClient(c.GlobalString("server"))

	pkgName := c.String("name")
	if len(pkgName) == 0 {
		return lib.MissingArgError("name")
	}
	pkgNamespace := c.String("pkgNamespace")

	pkg, err := client.PackageGet(&metav1.ObjectMeta{
		Name:      pkgName,
		Namespace: pkgNamespace,
	})
	if err != nil {
		return lib.FailedToError(err, "find package")
	}

	if pkg.Status.BuildStatus != fission.BuildStatusFailed {
		return lib.GeneralError(fmt.Sprintf("Package %v is not in %v state.",
			pkg.Metadata.Name, fission.BuildStatusFailed))
	}

	_, err = lib.UpdatePackage(client, pkg, "", "", "", "", "", true)
	if err != nil {
		return lib.FailedToError(err, "update package")
	}

	fmt.Printf("Retrying build for pkg %v. Use \"fission pkg info --name %v\" to view status.\n", pkg.Metadata.Name, pkg.Metadata.Name)

	return nil
}
