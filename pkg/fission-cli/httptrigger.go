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
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/satori/go.uuid"
	"github.com/urfave/cli"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/log"
	"github.com/fission/fission/pkg/fission-cli/util"
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

func setHtFunctionRef(functionList []string, functionWeightsList []int) (*fv1.FunctionReference, error) {
	if len(functionList) == 1 {
		return &fv1.FunctionReference{
			Type: fv1.FunctionReferenceTypeFunctionName,
			Name: functionList[0],
		}, nil
	} else if len(functionList) == 2 {
		if len(functionWeightsList) != 2 {
			return nil, fmt.Errorf("weights of the function need to be specified when 2 functions are supplied")
		}

		totalWeight := functionWeightsList[0] + functionWeightsList[1]
		if totalWeight != 100 {
			log.Fatal("The function weights should add up to 100")
		}

		functionWeights := make(map[string]int)
		for index := range functionList {
			functionWeights[functionList[index]] = functionWeightsList[index]
		}

		return &fv1.FunctionReference{
			Type:            fv1.FunctionReferenceTypeFunctionWeights,
			FunctionWeights: functionWeights,
		}, nil
	}

	return nil, fmt.Errorf("the number of functions in a trigger can be 1 or 2(for canary feature along with their weights)")
}

func htCreate(c *cli.Context) error {
	client := util.GetApiClient(c.GlobalString("server"))

	functionList := c.StringSlice("function")
	functionWeightsList := c.IntSlice("weight")

	if len(functionList) == 0 {
		log.Fatal("Need a function name to create a trigger, use --function")
	}

	functionRef, err := setHtFunctionRef(functionList, functionWeightsList)
	if err != nil {
		log.Fatal(err.Error())
	}

	triggerName := c.String("name")
	fnNamespace := c.String("fnNamespace")
	toSpec := c.Bool("spec")

	m := &metav1.ObjectMeta{
		Name:      triggerName,
		Namespace: fnNamespace,
	}

	htTrigger, err := client.HTTPTriggerGet(m)
	if err != nil && !ferror.IsNotFound(err) {
		log.Fatal(err.Error())
	}
	if htTrigger != nil {
		util.CheckErr(fmt.Errorf("duplicate trigger exists"), "choose a different name or leave it empty for fission to auto-generate it")
	}

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
	if !toSpec {
		err = util.CheckFunctionExistence(client, functionList, fnNamespace)
		if err != nil {
			log.Warn(err.Error())
		}
	}

	createIngress := false
	if c.IsSet("createingress") {
		createIngress = c.Bool("createingress")
	}

	host := c.String("host")

	// just name triggers by uuid.
	if triggerName == "" {
		triggerName = uuid.NewV4().String()
	}

	ht := &fv1.HTTPTrigger{
		Metadata: metav1.ObjectMeta{
			Name:      triggerName,
			Namespace: fnNamespace,
		},
		Spec: fv1.HTTPTriggerSpec{
			Host:              host,
			RelativeURL:       triggerUrl,
			Method:            getMethod(method),
			FunctionReference: *functionRef,
			CreateIngress:     createIngress,
		},
	}

	// if we're writing a spec, don't call the API
	if toSpec {
		specFile := fmt.Sprintf("route-%v.yaml", triggerName)
		err := spec.SpecSave(*ht, specFile)
		util.CheckErr(err, "create HTTP trigger spec")
		return nil
	}

	_, err = client.HTTPTriggerCreate(ht)
	util.CheckErr(err, "create HTTP trigger")

	fmt.Printf("trigger '%v' created\n", triggerName)
	return err
}

func htGet(c *cli.Context) error {
	cliClient := util.GetApiClient(c.GlobalString("server"))

	name := c.String("name")
	ns := c.String("fnNamespace")

	m := &metav1.ObjectMeta{
		Name:      name,
		Namespace: ns,
	}

	htTrigger, err := cliClient.HTTPTriggerGet(m)
	util.CheckErr(err, "get http trigger")

	w := tabwriter.NewWriter(os.Stdout, 0, 1, 1, ' ', 0)

	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "UID", "METHOD", "RELATIVE-URL", "FUNCTION-REFERENCE-TYPE", "FUNCTION(s)")

	function := ""
	if htTrigger.Spec.FunctionReference.Type == fv1.FunctionReferenceTypeFunctionName {
		function = htTrigger.Spec.FunctionReference.Name
	} else {
		for k, v := range htTrigger.Spec.FunctionReference.FunctionWeights {
			function += fmt.Sprintf("%s:%v ", k, v)
		}
	}

	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\n",
		htTrigger.Metadata.Name, htTrigger.Metadata.UID, htTrigger.Spec.Method, htTrigger.Spec.RelativeURL,
		htTrigger.Spec.FunctionReference.Type, function)

	w.Flush()

	return err
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
		// get the functions and their weights if specified
		functionList := c.StringSlice("function")
		err := util.CheckFunctionExistence(client, functionList, triggerNamespace)
		if err != nil {
			if err != nil {
				log.Warn(err.Error())
			}
		}

		var functionWeightsList []int
		if c.IsSet("weight") {
			functionWeightsList = c.IntSlice("weight")
		}

		// set function reference
		functionRef, err := setHtFunctionRef(functionList, functionWeightsList)
		if err != nil {
			log.Fatal(err.Error())
		}

		ht.Spec.FunctionReference = *functionRef
	}

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
