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

	"github.com/urfave/cli"
	"k8s.io/client-go/1.5/pkg/api"

	"github.com/fission/fission"
	"github.com/fission/fission/tpr"
)

func envCreate(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	envName := c.String("name")
	if len(envName) == 0 {
		fatal("Need a name, use --name.")
	}

	envImg := c.String("image")
	if len(envImg) == 0 {
		fatal("Need an image, use --image.")
	}

	env := &tpr.Environment{
		Metadata: api.ObjectMeta{
			Name:      envName,
			Namespace: api.NamespaceDefault,
		},
		Spec: fission.EnvironmentSpec{
			Version: 1,
			Runtime: fission.Runtime{
				Image: envImg,
			},
		},
	}

	_, err := client.EnvironmentCreate(env)
	checkErr(err, "create environment")

	fmt.Printf("environment '%v' created\n", envName)
	return err
}

func envGet(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	envName := c.String("name")
	if len(envName) == 0 {
		fatal("Need a name, use --name.")
	}

	m := &api.ObjectMeta{
		Name:      envName,
		Namespace: api.NamespaceDefault,
	}
	env, err := client.EnvironmentGet(m)
	checkErr(err, "get environment")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\n", "NAME", "UID", "IMAGE")
	fmt.Fprintf(w, "%v\t%v\t%v\n",
		env.Metadata.Name, env.Metadata.UID, env.Spec.Runtime.Image)
	w.Flush()
	return nil
}

func envUpdate(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	envName := c.String("name")
	if len(envName) == 0 {
		fatal("Need a name, use --name.")
	}

	envImg := c.String("image")
	if len(envImg) == 0 {
		fatal("Need an image, use --image.")
	}

	env, err := client.EnvironmentGet(&api.ObjectMeta{
		Name:      envName,
		Namespace: api.NamespaceDefault,
	})
	checkErr(err, "find environment")

	env.Spec.Runtime.Image = envImg

	_, err = client.EnvironmentUpdate(env)
	checkErr(err, "update environment")

	fmt.Printf("environment '%v' updated\n", envName)
	return nil
}

func envDelete(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	envName := c.String("name")
	if len(envName) == 0 {
		fatal("Need a name , use --name.")
	}

	m := &api.ObjectMeta{
		Name:      envName,
		Namespace: api.NamespaceDefault,
	}
	err := client.EnvironmentDelete(m)
	checkErr(err, "delete environment")

	fmt.Printf("environment '%v' deleted\n", envName)
	return nil
}

func envList(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	envs, err := client.EnvironmentList()
	checkErr(err, "list environments")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\n", "NAME", "UID", "IMAGE")
	for _, env := range envs {
		fmt.Fprintf(w, "%v\t%v\t%v\n",
			env.Metadata.Name, env.Metadata.UID, env.Spec.Runtime.Image)
	}
	w.Flush()

	return nil
}
