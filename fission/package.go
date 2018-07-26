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
	"github.com/fission/fission/fission/sdk"
)

func pkgCreate(c *cli.Context) error {
	client := sdk.GetClient(c.GlobalString("server"))

	pkgNamespace := c.String("pkgNamespace")
	envName := c.String("env")
	if len(envName) == 0 {
		LogAndExit("Need --env argument.")
	}
	envNamespace := c.String("envNamespace")
	srcArchiveName := c.String("src")
	deployArchiveName := c.String("deploy")
	buildcmd := c.String("buildcmd")

	if len(srcArchiveName) == 0 && len(deployArchiveName) == 0 {
		LogAndExit("Need --src to specify source archive, or use --deploy to specify deployment archive.")
	}

	meta, err := sdk.CreatePackage(client, pkgNamespace, envName, envNamespace, srcArchiveName, deployArchiveName, buildcmd, "")
	if err != nil {
		return sdk.FailedToError(err, "create package")
	}
	fmt.Printf("Package '%v' created\n", meta.GetName())

	return nil
}

func pkgUpdate(c *cli.Context) error {
	client := sdk.GetClient(c.GlobalString("server"))

	pkgName := c.String("name")
	if len(pkgName) == 0 {
		LogAndExit("Need --name argument.")
	}
	pkgNamespace := c.String("pkgNamespace")

	force := c.Bool("f")
	envName := c.String("env")
	envNamespace := c.String("envNamespace")
	srcArchiveName := c.String("src")
	deployArchiveName := c.String("deploy")
	buildcmd := c.String("buildcmd")

	if len(srcArchiveName) > 0 && len(deployArchiveName) > 0 {
		LogAndExit("Need either of --src or --deploy and not both arguments.")
	}

	if len(srcArchiveName) == 0 && len(deployArchiveName) == 0 &&
		len(envName) == 0 && len(buildcmd) == 0 {
		LogAndExit("Need --env or --src or --deploy or --buildcmd argument.")
	}

	pkg, err := client.PackageGet(&metav1.ObjectMeta{
		Namespace: pkgNamespace,
		Name:      pkgName,
	})
	if err != nil {
		return sdk.FailedToError(err, "get package")
	}

	fnList, err := sdk.GetFunctionsByPackage(client, pkg.Metadata.Name, pkg.Metadata.Namespace)
	if err != nil {
		return sdk.FailedToError(err, "get function list")
	}

	if !force && len(fnList) > 1 {
		LogAndExit("Package is used by multiple functions, use --force to force update")
	}

	newPkgMeta, err := sdk.UpdatePackage(client, pkg,
		envName, envNamespace, srcArchiveName, deployArchiveName, buildcmd)
	if err != nil {
		return sdk.FailedToError(err, "update package")
	}

	// update resource version of package reference of functions that shared the same package
	for _, fn := range fnList {
		fn.Spec.Package.PackageRef.ResourceVersion = newPkgMeta.ResourceVersion
		_, err := client.FunctionUpdate(&fn)
		if err != nil {
			return sdk.FailedToError(err, "update function")
		}
	}

	fmt.Printf("Package '%v' updated\n", newPkgMeta.GetName())

	return nil
}

func pkgSourceGet(c *cli.Context) error {
	client := sdk.GetClient(c.GlobalString("server"))

	pkgName := c.String("name")
	if len(pkgName) == 0 {
		LogAndExit("Need name of package, use --name")
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
		readCloser := sdk.DownloadStoragesvcURL(client, pkg.Spec.Source.URL)
		defer readCloser.Close()
		reader = readCloser
	}

	if len(output) > 0 {
		return sdk.WriteArchiveToFile(output, reader)
	} else {
		_, err := io.Copy(os.Stdout, reader)
		return err
	}
}

func pkgDeployGet(c *cli.Context) error {
	client := sdk.GetClient(c.GlobalString("server"))

	pkgName := c.String("name")
	if len(pkgName) == 0 {
		LogAndExit("Need name of package, use --name")
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
		readCloser := sdk.DownloadStoragesvcURL(client, pkg.Spec.Deployment.URL)
		defer readCloser.Close()
		reader = readCloser
	}

	if len(output) > 0 {
		return sdk.WriteArchiveToFile(output, reader)
	} else {
		_, err := io.Copy(os.Stdout, reader)
		return err
	}
}

func pkgInfo(c *cli.Context) error {
	client := sdk.GetClient(c.GlobalString("server"))

	pkgName := c.String("name")
	if len(pkgName) == 0 {
		LogAndExit("Need name of package, use --name")
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
	client := sdk.GetClient(c.GlobalString("server"))
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
			fnList, err := sdk.GetFunctionsByPackage(client, pkg.Metadata.Name, pkg.Metadata.Namespace)
			if err != nil {
				return sdk.FailedToError(err, fmt.Sprintf("get functions sharing package %s", pkg.Metadata.Name))
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
	client := sdk.GetClient(c.GlobalString("server"))

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
			return sdk.FailedToError(err, "find package")
		}

		fnList, err := sdk.GetFunctionsByPackage(client, pkgName, pkgNamespace)

		if !force && len(fnList) > 0 {
			LogAndExit("Package is used by at least one function, use -f to force delete")
		}

		err = sdk.DeletePackage(client, pkgName, pkgNamespace)
		if err != nil {
			return err
		}

		fmt.Printf("Package '%v' deleted\n", pkgName)
	} else {
		err := sdk.DeleteOrphanPkgs(client, pkgNamespace)
		if err != nil {
			return sdk.FailedToError(err, "error deleting orphan packages")
		}
		fmt.Println("Orphan packages deleted")
	}

	return nil
}
