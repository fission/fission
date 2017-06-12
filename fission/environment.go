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

	"github.com/fission/fission"
	"github.com/fission/fission/poolmgr"
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

	envCpuLimit := c.String("cpulimit")
	if len(envCpuLimit) == 0 {
		envCpuLimit = poolmgr.DEFAULT_CPU_LIMIT.String()
	}
	envMemLimit := c.String("memlimit")
	if len(envMemLimit) == 0 {
		envMemLimit = poolmgr.DEFAULT_MEM_LIMIT.String()
	}
	envCpuRequest := c.String("cpurequest")
	if len(envCpuRequest) == 0 {
		envCpuRequest = poolmgr.DEFAULT_CPU_REQUEST.String()
	}
	envMemRequest := c.String("memrequest")
	if len(envMemRequest) == 0 {
		envMemRequest = poolmgr.DEFAULT_MEM_REQUEST.String()
	}

	env := &fission.Environment{
		Metadata: fission.Metadata{
			Name: envName,
		},
		RunContainerImageUrl: envImg,
		CpuLimit:             envCpuLimit,
		MemLimit:             envMemLimit,
		CpuRequest:           envCpuRequest,
		MemRequest:           envMemRequest,
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

	m := &fission.Metadata{Name: envName}
	env, err := client.EnvironmentGet(m)
	checkErr(err, "get environment")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "UID", "IMAGE", "CPU_LIMIT", "CPU_REQUEST", "MEM_LIMIT", "MEM_REQUEST")
	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
		env.Metadata.Name, env.Metadata.Uid, env.RunContainerImageUrl,
		env.CpuLimit, env.CpuRequest, env.MemLimit, env.MemRequest)
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

	envCpuLimit := c.String("cpulimit")
	if len(envCpuLimit) == 0 {
		envCpuLimit = poolmgr.DEFAULT_CPU_LIMIT.String()
	}
	envMemLimit := c.String("memlimit")
	if len(envMemLimit) == 0 {
		envMemLimit = poolmgr.DEFAULT_MEM_LIMIT.String()
	}
	envCpuRequest := c.String("cpurequest")
	if len(envCpuRequest) == 0 {
		envCpuRequest = poolmgr.DEFAULT_CPU_REQUEST.String()
	}
	envMemRequest := c.String("memrequest")
	if len(envMemRequest) == 0 {
		envMemRequest = poolmgr.DEFAULT_MEM_REQUEST.String()
	}

	env := &fission.Environment{
		Metadata: fission.Metadata{
			Name: envName,
		},
		RunContainerImageUrl: envImg,
		CpuLimit:             envCpuLimit,
		MemLimit:             envMemLimit,
		CpuRequest:           envCpuRequest,
		MemRequest:           envMemRequest,
	}

	_, err := client.EnvironmentUpdate(env)
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

	m := &fission.Metadata{Name: envName}
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
	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "UID", "IMAGE", "CPU_LIMIT", "CPU_REQUEST", "MEM_LIMIT", "MEM_REQUEST")
	for _, env := range envs {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
			env.Metadata.Name, env.Metadata.Uid, env.RunContainerImageUrl,
			env.CpuLimit, env.CpuRequest, env.MemLimit, env.MemRequest)
	}
	w.Flush()

	return nil
}
