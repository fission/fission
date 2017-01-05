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
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/satori/go.uuid"
	"github.com/urfave/cli"

	"github.com/fission/fission"
)

// returns one of http.Method*
func getMethod(method string) string {
	switch strings.ToUpper(method) {
	case "GET":
		return http.MethodGet
	case "HEAD":
		return http.MethodHead
	case "POST":
		return http.MethodPost
	case "PUT":
		return http.MethodPut
	case "PATCH":
		return http.MethodPatch
	case "DELETE":
		return http.MethodDelete
	case "CONNECT":
		return http.MethodConnect
	case "OPTIONS":
		return http.MethodOptions
	case "TRACE":
		return http.MethodTrace
	}
	fatal(fmt.Sprintf("Invalid HTTP Method %v", method))
	return ""
}

func htCreate(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	fnName := c.String("function")
	if len(fnName) == 0 {
		fatal("Need a function name to create a trigger, use --function")
	}
	fnUid := c.String("uid")
	triggerUrl := c.String("url")
	if len(triggerUrl) == 0 {
		fatal("Need a trigger URL, use --url")
	}
	method := c.String("method")
	if len(method) == 0 {
		method = "GET"
	}

	// just name triggers by uuid.
	triggerName := uuid.NewV4().String()

	ht := &fission.HTTPTrigger{
		Metadata: fission.Metadata{
			Name: triggerName,
		},
		UrlPattern: triggerUrl,
		Method:     getMethod(method),
		Function: fission.Metadata{
			Name: fnName,
			Uid:  fnUid,
		},
	}

	_, err := client.HTTPTriggerCreate(ht)
	checkErr(err, "create HTTP trigger")

	fmt.Printf("trigger '%v' created\n", triggerName)
	return err
}

func htGet(c *cli.Context) error {
	return nil
}

func htUpdate(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))
	htName := c.String("name")
	if len(htName) == 0 {
		fatal("Need name of trigger, use --name")
	}

	ht, err := client.HTTPTriggerGet(&fission.Metadata{Name: htName})
	checkErr(err, "get HTTP trigger")

	newUid := c.String("uid")
	ht.Function.Uid = newUid

	_, err = client.HTTPTriggerUpdate(ht)
	checkErr(err, "update HTTP trigger")

	fmt.Printf("trigger '%v' updated\n", htName)
	return nil
}

func htDelete(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))
	htName := c.String("name")
	if len(htName) == 0 {
		fatal("Need name of trigger to delete, use --name")
	}

	err := client.HTTPTriggerDelete(&fission.Metadata{Name: htName})
	checkErr(err, "delete trigger")

	fmt.Printf("trigger '%v' deleted\n", htName)
	return nil
}

func htList(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	hts, err := client.HTTPTriggerList()
	checkErr(err, "list HTTP triggers")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)

	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n", "NAME", "METHOD", "URL", "FUNCTION_NAME", "FUNCTION_UID")
	for _, ht := range hts {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n",
			ht.Metadata.Name, ht.Method, ht.UrlPattern, ht.Function.Name, ht.Function.Uid)
	}
	w.Flush()

	return nil
}
