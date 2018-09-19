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

	"github.com/fission/fission/fission/util"
	"github.com/satori/go.uuid"
	"github.com/urfave/cli"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission"
	"github.com/fission/fission/controller/client"
	"github.com/fission/fission/crd"
	"github.com/fission/fission/fission/log"
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
	log.Fatal(fmt.Sprintf("Invalid HTTP Method %v", method))
	return ""
}

func checkFunctionExistence(fissionClient *client.Client, fnName string, fnNamespace string) {
	meta := &metav1.ObjectMeta{
		Name:      fnName,
		Namespace: fnNamespace,
	}

	_, err := fissionClient.FunctionGet(meta)
	if err != nil {
		fmt.Printf("function '%v' does not exist, use 'fission function create --name %v ...' to create the function\n", fnName, fnName)
	}
}

func htCreate(c *cli.Context) error {
	client := util.GetApiClient(c.GlobalString("server"))

	fnName := c.String("function")
	if len(fnName) == 0 {
		log.Fatal("Need a function name to create a trigger, use --function")
	}
	fnNamespace := c.String("fnNamespace")
	spec := c.Bool("spec")

	triggerUrl := c.String("url")
	if len(triggerUrl) == 0 {
		log.Fatal("Need a trigger URL, use --url")
	}
	if !strings.HasPrefix(triggerUrl, "/") {
		triggerUrl = fmt.Sprintf("/%s", triggerUrl)
	}

	method := c.String("method")
	if len(method) == 0 {
		method = "GET"
	}

	// For Specs, the spec validate checks for function reference
	if !spec {
		checkFunctionExistence(client, fnName, fnNamespace)
	}

	createIngress := false
	if c.IsSet("createingress") {
		createIngress = c.Bool("createingress")
	}

	host := c.String("host")

	// just name triggers by uuid.
	triggerName := uuid.NewV4().String()

	ht := &crd.HTTPTrigger{
		Metadata: metav1.ObjectMeta{
			Name:      triggerName,
			Namespace: fnNamespace,
		},
		Spec: fission.HTTPTriggerSpec{
			Host:        host,
			RelativeURL: triggerUrl,
			Method:      getMethod(method),
			FunctionReference: fission.FunctionReference{
				Type: fission.FunctionReferenceTypeFunctionName,
				Name: fnName,
			},
			CreateIngress: createIngress,
		},
	}

	// if we're writing a spec, don't call the API
	if spec {
		specFile := fmt.Sprintf("route-%v.yaml", triggerName)
		err := specSave(*ht, specFile)
		util.CheckErr(err, "create HTTP trigger spec")
		return nil
	}

	_, err := client.HTTPTriggerCreate(ht)
	util.CheckErr(err, "create HTTP trigger")

	fmt.Printf("trigger '%v' created\n", triggerName)
	return err
}

func htGet(c *cli.Context) error {
	return nil
}

func htUpdate(c *cli.Context) error {
	client := util.GetApiClient(c.GlobalString("server"))
	htName := c.String("name")
	if len(htName) == 0 {
		log.Fatal("Need name of trigger, use --name")
	}
	triggerNamespace := c.String("triggerNamespace")

	ht, err := client.HTTPTriggerGet(&metav1.ObjectMeta{
		Name:      htName,
		Namespace: triggerNamespace,
	})
	util.CheckErr(err, "get HTTP trigger")

	if c.IsSet("function") {
		ht.Spec.FunctionReference.Name = c.String("function")
	}
	checkFunctionExistence(client, ht.Spec.FunctionReference.Name, triggerNamespace)

	if c.IsSet("createingress") {
		ht.Spec.CreateIngress = c.Bool("createingress")
	}

	if c.IsSet("host") {
		ht.Spec.Host = c.String("host")
	}

	_, err = client.HTTPTriggerUpdate(ht)
	util.CheckErr(err, "update HTTP trigger")

	fmt.Printf("trigger '%v' updated\n", htName)
	return nil
}

func htDelete(c *cli.Context) error {
	client := util.GetApiClient(c.GlobalString("server"))
	htName := c.String("name")
	if len(htName) == 0 {
		log.Fatal("Need name of trigger to delete, use --name")
	}
	triggerNamespace := c.String("triggerNamespace")

	err := client.HTTPTriggerDelete(&metav1.ObjectMeta{
		Name:      htName,
		Namespace: triggerNamespace,
	})
	util.CheckErr(err, "delete trigger")

	fmt.Printf("trigger '%v' deleted\n", htName)
	return nil
}

func htList(c *cli.Context) error {
	client := util.GetApiClient(c.GlobalString("server"))
	triggerNamespace := c.String("triggerNamespace")

	hts, err := client.HTTPTriggerList(triggerNamespace)
	util.CheckErr(err, "list HTTP triggers")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)

	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "METHOD", "HOST", "URL", "INGRESS", "FUNCTION_NAME")
	for _, ht := range hts {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\n",
			ht.Metadata.Name, ht.Spec.Method, ht.Spec.Host, ht.Spec.RelativeURL, ht.Spec.CreateIngress, ht.Spec.FunctionReference.Name)
	}
	w.Flush()

	return nil
}
