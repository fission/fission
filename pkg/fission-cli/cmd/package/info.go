package _package

import (
	"fmt"
	"os"
	"text/tabwriter"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	cmdutils "github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/log"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type InfoSubCommand struct {
	client    *client.Client
	name      string
	namespace string
}

func Info(flags cli.Input) error {
	opts := InfoSubCommand{
		client: cmdutils.GetServer(flags),
	}
	return opts.do(flags)
}

func (opts *InfoSubCommand) do(flags cli.Input) error {
	err := opts.complete(flags)
	if err != nil {
		return err
	}
	return opts.run(flags)
}

func (opts *InfoSubCommand) complete(flags cli.Input) error {
	opts.name = flags.String("name")
	if len(opts.name) == 0 {
		log.Fatal("Need name of package, use --name")
	}
	opts.namespace = flags.String("pkgNamespace")
	return nil
}

func (opts *InfoSubCommand) run(flags cli.Input) error {
	pkg, err := opts.client.PackageGet(&metav1.ObjectMeta{
		Namespace: opts.namespace,
		Name:      opts.name,
	})
	if err != nil {
		util.CheckErr(err, fmt.Sprintf("find package %s", opts.name))
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\n", "Name:", pkg.Metadata.Name)
	fmt.Fprintf(w, "%v\t%v\n", "Environment:", pkg.Spec.Environment.Name)
	fmt.Fprintf(w, "%v\t%v\n", "Status:", pkg.Status.BuildStatus)
	fmt.Fprintf(w, "%v\n%v", "Build Logs:", pkg.Status.BuildLog)
	w.Flush()

	return nil
}
