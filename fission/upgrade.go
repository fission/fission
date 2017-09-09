package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/urfave/cli"
	"k8s.io/client-go/1.5/pkg/api"

	"github.com/fission/fission"
	"github.com/fission/fission/tpr"
	"github.com/fission/fission/v1"
)

type (
	V1FissionState struct {
		functions    []v1.Function            `json:"functions"`
		environments []v1.Environment         `json:"environments"`
		httptriggers []v1.HTTPTrigger         `json:"httptriggers"`
		mqtriggers   []v1.MessageQueueTrigger `json:"mqtriggers"`
		timetriggers []v1.TimeTrigger         `json:"timetriggers"`
		watches      []v1.Watch               `json:"watches"`
	}
)

func getV1URL(serverUrl string) string {
	if len(serverUrl) == 0 {
		fatal("Need --server or FISSION_URL set to your fission server.")
	}
	isHTTPS := strings.Index(serverUrl, "https://") == 0
	isHTTP := strings.Index(serverUrl, "http://") == 0
	if !(isHTTP || isHTTPS) {
		serverUrl = "http://" + serverUrl
	}
	v1url := strings.TrimSuffix(serverUrl, "/") + "/v1"
	return v1url
}

func get(url string) []byte {
	resp, err := http.Get(url)
	checkErr(err, "get fission v0.1 state")
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	checkErr(err, "reading server response")

	if resp.StatusCode != 200 {
		fatal(fmt.Sprintf("Failed to fetch fission v0.1 state: %v", string(body)))
	}
	return body
}

func upgradeDumpV1State(v1url string) {
	var v1state V1FissionState

	resp := get(v1url + "/environments")
	err := json.Unmarshal(resp, &v1state.environments)
	checkErr(err, "parse server response")

	resp = get(v1url + "/watches")
	err = json.Unmarshal(resp, &v1state.watches)
	checkErr(err, "parse server response")

	resp = get(v1url + "/triggers/http")
	err = json.Unmarshal(resp, &v1state.httptriggers)
	checkErr(err, "parse server response")

	resp = get(v1url + "/triggers/messagequeue")
	err = json.Unmarshal(resp, &v1state.mqtriggers)
	checkErr(err, "parse server response")

	resp = get(v1url + "/triggers/time")
	err = json.Unmarshal(resp, &v1state.timetriggers)
	checkErr(err, "parse server response")

	resp = get(v1url + "/functions")
	err = json.Unmarshal(resp, &v1state.functions)
	checkErr(err, "parse server response")

	funcMetaSet := make(map[v1.Metadata]bool)
	for _, f := range v1state.functions {
		funcMetaSet[f.Metadata] = true
	}
	for _, t := range v1state.httptriggers {
		funcMetaSet[t.Function] = true
	}
	for _, t := range v1state.mqtriggers {
		funcMetaSet[t.Function] = true
	}
	for _, t := range v1state.watches {
		funcMetaSet[t.Function] = true
	}
	for _, t := range v1state.timetriggers {
		funcMetaSet[t.Function] = true
	}

	// get each function
	v1state.functions = nil
	for m, _ := range funcMetaSet {
		resp = get(fmt.Sprintf("%v/functions/%v", v1url, m.Name))
		var f v1.Function
		// unmarshal
		err = json.Unmarshal(resp, &f)
		checkErr(err, "parse server response")
		// add to v1state list
		v1state.functions = append(v1state.functions, f)
	}

	// serialize v1state
	out, err := json.Marshal(v1state)
	checkErr(err, "serializing v0.1 state")

	// dump to file fission-v01-state.json
	err = ioutil.WriteFile("fission-v01-state.json", out, 0644)
	checkErr(err, "writing file")
}

func upgradeDumpState(c *cli.Context) error {
	u := getV1URL(c.GlobalString("server"))
	upgradeDumpV1State(u)
	return nil
}

func upgradeRestoreState(c *cli.Context) error {
	filename := "fission-v01-state.json"
	contents, err := ioutil.ReadFile(filename)
	checkErr(err, fmt.Sprintf("open file %v", filename))

	var v1state V1FissionState
	err = json.Unmarshal(contents, &v1state)
	checkErr(err, "parse dumped v1 state")

	// create a regular v2 client
	client := getClient(c.GlobalString("server"))

	// create envs
	for _, e := range v1state.environments {
		_, err = client.EnvironmentCreate(&tpr.Environment{
			Metadata: api.ObjectMeta{
				Name:      e.Metadata.Name,
				Namespace: api.NamespaceDefault,
			},
			Spec: fission.EnvironmentSpec{
				Version: 1,
				Runtime: fission.Runtime{
					Image: e.RunContainerImageUrl,
				},
			},
		})
		checkErr(err, fmt.Sprintf("create environment %v", e.Metadata))
	}

	// create httptriggers
	// create mqtriggers
	// create time triggers
	// create watches
	// create functions

	return nil
}
