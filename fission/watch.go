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

package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/satori/go.uuid"
	"github.com/urfave/cli"

	"github.com/fission/fission"
)

func wCreate(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	fnName := c.String("function")
	if len(fnName) == 0 {
		fatal("Need a function name to create a watch, use --function")
	}
	fnUid := c.String("uid")

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
	}

	// automatically name watches
	watchName := uuid.NewV4().String()

	w := &fission.Watch{
		Metadata: fission.Metadata{
			Name: watchName,
		},
		Function: fission.Metadata{
			Name: fnName,
			Uid:  fnUid,
		},
		Namespace:     namespace,
		ObjType:       objType,
		LabelSelector: labels,
		FieldSelector: "", // TODO
	}

	_, err := client.WatchCreate(w)
	checkErr(err, "create watch")

	fmt.Printf("watch '%v' created\n", w.Metadata.Name)
	return err
}

func wGet(c *cli.Context) error {
	return nil
}

func wUpdate(c *cli.Context) error {
	return nil
}

func wDelete(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	wName := c.String("name")
	if len(wName) == 0 {
		fatal("Need name of watch to delete, use --name")
	}

	err := client.WatchDelete(&fission.Metadata{Name: wName})
	checkErr(err, "delete watch")

	fmt.Printf("watch '%v' deleted\n", wName)
	return nil
}

func wList(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	ws, err := client.WatchList()
	checkErr(err, "list watches")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)

	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "NAMESPACE", "OBJTYPE", "LABELS", "FUNCTION_NAME", "FUNCTION_UID")
	for _, wa := range ws {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\n",
			wa.Metadata.Name, wa.Namespace, wa.ObjType, wa.LabelSelector, wa.Function.Name, wa.Function.Uid)
	}
	w.Flush()

	return nil
}
