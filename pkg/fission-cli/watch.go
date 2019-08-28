/*
Copyright 2016 The Fission Authors.

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

package fission_cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/satori/go.uuid"
	"github.com/urfave/cli"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/log"
	"github.com/fission/fission/pkg/fission-cli/util"
)

func wCreate(c *cli.Context) error {
	client := util.GetApiClient(c.GlobalString("server"))

	fnName := c.String("function")
	if len(fnName) == 0 {
		log.Fatal("Need a function name to create a watch, use --function")
	}
	fnNamespace := c.String("fnNamespace")

	namespace := c.String("ns")
	if len(namespace) == 0 {
		fmt.Println("Watch 'default' namespace. Use --ns <namespace> to override.")
		namespace = "default"
	}

	objType := c.String("type")
	if len(objType) == 0 {
		fmt.Println("Object type unspecified, will watch pods.  Use --type <type> to override.")
		objType = "pod"
	}

	labels := c.String("labels")
	// empty 'labels' selects everything
	if len(labels) == 0 {
		fmt.Printf("Watching all objects of type '%v', use --labels to refine selection.\n", objType)
	} else {
		// TODO
		fmt.Printf("Label selector not implemented, watching all objects")
	}

	// automatically name watches
	watchName := uuid.NewV4().String()

	w := &fv1.KubernetesWatchTrigger{
		Metadata: metav1.ObjectMeta{
			Name:      watchName,
			Namespace: fnNamespace,
		},
		Spec: fv1.KubernetesWatchTriggerSpec{
			Namespace: namespace,
			Type:      objType,
			//LabelSelector: labels,
			FunctionReference: fv1.FunctionReference{
				Name: fnName,
				Type: fv1.FunctionReferenceTypeFunctionName,
			},
		},
	}

	// if we're writing a spec, don't call the API
	if c.Bool("spec") {
		specFile := fmt.Sprintf("kubewatch-%v.yaml", watchName)
		err := spec.SpecSave(*w, specFile)
		util.CheckErr(err, "create kubernetes watch spec")
		return nil
	}

	_, err := client.WatchCreate(w)
	util.CheckErr(err, "create watch")

	fmt.Printf("watch '%v' created\n", w.Metadata.Name)
	return err
}

func wGet(c *cli.Context) error {
	// TODO
	log.Fatal("Not implemented")
	return nil
}

func wDelete(c *cli.Context) error {
	client := util.GetApiClient(c.GlobalString("server"))

	wName := c.String("name")
	if len(wName) == 0 {
		log.Fatal("Need name of watch to delete, use --name")
	}
	wNs := c.String("triggerns")

	err := client.WatchDelete(&metav1.ObjectMeta{
		Name:      wName,
		Namespace: wNs,
	})
	util.CheckErr(err, "delete watch")

	fmt.Printf("watch '%v' deleted\n", wName)
	return nil
}

func wList(c *cli.Context) error {
	client := util.GetApiClient(c.GlobalString("server"))

	wNs := c.String("triggerns")

	ws, err := client.WatchList(wNs)
	util.CheckErr(err, "list watches")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)

	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n",
		"NAME", "NAMESPACE", "OBJTYPE", "LABELS", "FUNCTION_NAME")
	for _, wa := range ws {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n",
			wa.Metadata.Name, wa.Spec.Namespace, wa.Spec.Type, wa.Spec.LabelSelector, wa.Spec.FunctionReference.Name)
	}
	w.Flush()

	return nil
}
