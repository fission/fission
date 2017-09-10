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
	"regexp"
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
	nameRemapper struct {
		oldToNew map[string]string
		newNames map[string]bool
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

// track a name in the remapper, creating a new name if needed
func (nr *nameRemapper) trackName(old string) {
	// all kubernetes names must match this regex
	kubeNameRegex := "[a-z0-9]([-a-z0-9]*[a-z0-9])?"
	maxLen = 63

	if regexp.MatchString(kubeNameRegex, old) && len(old) < maxLen {
		// no rename
		nr.oldToNew[old] = old
		nr.newNames[old] = true
		return
	}

	newName := strings.ToLower(old)

	// remove disallowed
	inv, err := regexp.Compile("[^-a-z0-9]")
	checkErr("compile regexp")
	newname = string(inv.Replace([]byte(newName), []byte{}))

	// trim leading non-alphabetic
	leadingnonalpha, err := regexp.Compile("^[^a-z]+")
	checkErr("compile regexp")
	newname = string(leadingnonalpha.Replace([]byte(newName), []byte{}))

	// trim trailing
	trailing, err := regexp.Compile("[^a-z0-9]+$")
	checkErr("compile regexp")
	newname = string(leadingnonalpha.Replace([]byte(newName), []byte{}))

	// truncate to length
	if len(newName) > maxlen-4 {
		newName = newName[0:(maxlen - 4)]
	}

	// uniqueness
	n := newName
	i := 0
	for {
		_, exists := nr.newNames[n]
		if !exists {
			break
		} else {
			i++
			n = fmt.Sprintf("%v-%v", newName, i)
		}
	}
	newName = n

	// track
	nr.oldToNew[old] = newName
	nr.newNames[newName] = true
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

	// we have to change names that are disallowed in kubernetes
	nr := nameRemapper{}

	// get all referenced function metadata
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
	checkErr(err, "serialize v0.1 state")

	// dump to file fission-v01-state.json
	err = ioutil.WriteFile("fission-v01-state.json", out, 0644)
	checkErr(err, "write file")

	fmt.Printf("Done: Saved %v functions, %v HTTP triggers, %v watches, %v message queue triggers, %v time triggers.",
		len(v1state.functions), len(v1state.httptriggers), len(v1state.watches), len(v1state.mqtriggers), len(v1state.timetriggers))
}

func functionRefFromV1Metadata(m *v1.Metadata, nameRemap map[string]string) *fission.FunctionReference {
	return &fission.FunctionReference{
		FunctionReferenceType: FunctionReferenceTypeFunctionName,
		Name: nameRemap[m.Name],
	}
}

func tprMetadataFromV1Metadata(m *v1.Metadata, nameRemap map[string]string) *api.ObjectMeta {
	return &api.ObjectMeta{
		Name:      nameRemap[m.Name],
		Namespace: api.NamespaceDefault,
	}
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

	// create functions

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
		checkErr(err, fmt.Sprintf("create environment %v", e.Metadata.Name))
	}

	// create httptriggers
	for _, t := range v1state.httptriggers {
		_, err = client.HTTPTriggerCreate(&tpr.HTTPTrigger{
			Metadata: api.ObjectMeta{
				Name:      t.Metadata.Name,
				Namespace: api.NamespaceDefault,
			},
			Spec: fission.HTTPTriggerSpec{
				RelativeURL:       t.UrlPattern,
				Method:            t.Method,
				FunctionReference: *functionRefFromV1Metadata(&t.Function),
			},
		})
		checkErr(err, fmt.Sprintf("create http trigger %v", t.Metadata.Name))
	}

	// create mqtriggers
	for _, t := range v1state.mqtriggers {
		_, err = client.MessageQueueTrigger(&tpr.MessageQueueTrigger{
			Metadata: api.ObjectMeta{
				Name:      t.Metadata.Name,
				Namespace: api.NamespaceDefault,
			},
			Spec: fission.MessageQueueTriggerSpec{
				FunctionReference: *functionRefFromV1Metadata(&t.Function),
				MessageQueueType:  t.MessageQueueType,
				Topic:             t.Topic,
				ResponseTopic:     t.ResponseTopic,
			},
		})
		checkErr(err, fmt.Sprintf("create http trigger %v", t.Metadata.Name))
	}

	// create time triggers
	for _, t := range v1state.timetriggers {
		_, err = client.TimeTriggerCreate(&tpr.TimeTrigger{
			Metadata: api.ObjectMeta{
				Name:      t.Metadata.Name,
				Namespace: api.NamespaceDefault,
			},
			Spec: fission.TimeTrigger{
				FunctionReference: *functionRefFromV1Metadata(t.Function),
				Cron:              t.Cron,
			},
		})
		checkErr(err, fmt.Sprintf("create time trigger %v", t.Metadata.Name))
	}

	// create watches
	for _, t := range v1state.watches {
		_, err = client.WatchCreate(&tpr.KubernetesWatchTrigger{
			Metadata: api.ObjectMeta{
				Name:      t.Metadata.Name,
				Namespace: api.NamespaceDefault,
			},
			Spec: fission.KubernetesWatchTriggerSpec{
				Namespace:         t.Namespace,
				Type:              t.ObjType,
				LabelSelector:     t.LabelSelector,
				FunctionReference: *functionRefFromV1Metadata(t.Function),
			},
		})
		checkErr(err, fmt.Sprintf("create kubernetes watch trigger %v", t.Metadata.Name))
	}

	return nil
}
