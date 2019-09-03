package _package

import (
	"fmt"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	cmdutils "github.com/fission/fission/pkg/fission-cli/cmd"
)

type DeleteSubCommand struct {
	client        *client.Client
	name          string
	namespace     string
	deleteOrphans bool
	force         bool
}

func Delete(flags cli.Input) error {
	opts := DeleteSubCommand{
		client: cmdutils.GetServer(flags),
	}
	return opts.do(flags)
}

func (opts *DeleteSubCommand) do(flags cli.Input) error {
	err := opts.complete(flags)
	if err != nil {
		return err
	}
	return opts.run(flags)
}

func (opts *DeleteSubCommand) complete(flags cli.Input) error {
	opts.name = flags.String("name")
	opts.namespace = flags.String("pkgNamespace")
	opts.deleteOrphans = flags.Bool("orphan")
	opts.force = flags.Bool("f")

	if len(opts.name) == 0 && !opts.deleteOrphans {
		return errors.New("need --name argument or --orphan flag")
	}
	if len(opts.name) != 0 && opts.deleteOrphans {
		return errors.New("need either --name argument or --orphan flag")
	}

	return nil
}

func (opts *DeleteSubCommand) run(flags cli.Input) error {
	if len(opts.name) != 0 {
		_, err := opts.client.PackageGet(&metav1.ObjectMeta{
			Namespace: opts.namespace,
			Name:      opts.name,
		})
		if err != nil {
			return errors.Wrap(err, "find package")
		}

		fnList, err := GetFunctionsByPackage(opts.client, opts.name, opts.namespace)
		if err != nil {
			return err
		}

		if !opts.force && len(fnList) > 0 {
			return errors.New("Package is used by at least one function, use -f to force delete")
		}
		err = deletePackage(opts.client, opts.name, opts.namespace)
		if err != nil {
			return err
		}
		fmt.Printf("Package '%v' deleted\n", opts.name)
	} else {
		err := deleteOrphanPkgs(opts.client, opts.namespace)
		if err != nil {
			return errors.Wrap(err, "deleting orphan packages")
		}
		fmt.Println("Orphan packages deleted")
	}

	return nil
}

func deleteOrphanPkgs(client *client.Client, pkgNamespace string) error {
	pkgList, err := client.PackageList(pkgNamespace)
	if err != nil {
		return err
	}

	// range through all packages and find out the ones not referenced by any function
	for _, pkg := range pkgList {
		fnList, err := GetFunctionsByPackage(client, pkg.Metadata.Name, pkgNamespace)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("get functions sharing package %s", pkg.Metadata.Name))
		}
		if len(fnList) == 0 {
			err = deletePackage(client, pkg.Metadata.Name, pkgNamespace)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func deletePackage(client *client.Client, pkgName string, pkgNamespace string) error {
	return client.PackageDelete(&metav1.ObjectMeta{
		Namespace: pkgNamespace,
		Name:      pkgName,
	})
}
