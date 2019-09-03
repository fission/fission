package _package

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/pkg/errors"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	cmdutils "github.com/fission/fission/pkg/fission-cli/cmd"
)

type ListSubCommand struct {
	client       *client.Client
	listOrphans  bool
	status       string
	pkgNamespace string
	pkgName      string
}

func List(flags cli.Input) error {
	opts := ListSubCommand{
		client: cmdutils.GetServer(flags),
	}
	return opts.do(flags)
}

func (opts *ListSubCommand) do(flags cli.Input) error {
	err := opts.complete(flags)
	if err != nil {
		return err
	}
	return opts.run(flags)
}

func (opts *ListSubCommand) complete(flags cli.Input) error {
	// option for the user to list all orphan packages (not referenced by any function)
	opts.listOrphans = flags.Bool("orphan")
	opts.status = flags.String("status")
	opts.pkgNamespace = flags.String("pkgNamespace")
	return nil
}

func (opts *ListSubCommand) run(flags cli.Input) error {
	pkgList, err := opts.client.PackageList(opts.pkgNamespace)
	if err != nil {
		return err
	}

	// sort the package list by lastUpdatedTimestamp
	sort.Slice(pkgList, func(i, j int) bool {
		return pkgList[i].Status.LastUpdateTimestamp.After(pkgList[j].Status.LastUpdateTimestamp)
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", "NAME", "BUILD_STATUS", "ENV", "LASTUPDATEDAT")

	for _, pkg := range pkgList {
		show := true
		if opts.listOrphans {
			fnList, err := GetFunctionsByPackage(opts.client, pkg.Metadata.Name, pkg.Metadata.Namespace)
			if err != nil {
				return errors.Wrap(err, fmt.Sprintf("get functions sharing package %s", pkg.Metadata.Name))
			}
			if len(fnList) > 0 {
				show = false
			}
		}
		if len(opts.status) > 0 && opts.status != string(pkg.Status.BuildStatus) {
			show = false
		}
		if show {
			fmt.Fprintf(w, "%v\t%v\t%v\n", pkg.Metadata.Name, pkg.Status.BuildStatus, pkg.Spec.Environment.Name)
		}
	}

	w.Flush()

	return nil
}
