package sdk

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission"
	"github.com/fission/fission/fission/log"
	"github.com/fission/fission/v1"
)

type (
	V1FissionState struct {
		Functions    []v1.Function            `json:"functions"`
		Environments []v1.Environment         `json:"environments"`
		HTTPTriggers []v1.HTTPTrigger         `json:"httptriggers"`
		Mqtriggers   []v1.MessageQueueTrigger `json:"mqtriggers"`
		TimeTriggers []v1.TimeTrigger         `json:"timetriggers"`
		Watches      []v1.Watch               `json:"watches"`
		NameChanges  map[string]string        `json:"namechanges"`
	}
	nameRemapper struct {
		oldToNew map[string]string
		newNames map[string]bool
	}
)

func GetV1URL(serverUrl string) string {
	if len(serverUrl) == 0 {
		log.Fatal("Need --server or FISSION_URL set to your fission server.")
	}
	isHTTPS := strings.Index(serverUrl, "https://") == 0
	isHTTP := strings.Index(serverUrl, "http://") == 0
	if !(isHTTP || isHTTPS) {
		serverUrl = "http://" + serverUrl
	}
	v1url := strings.TrimSuffix(serverUrl, "/") + "/v1"
	return v1url
}

func Get(url string) []byte {
	resp, err := http.Get(url)
	CheckErr(err, "get fission v0.1 state")
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	CheckErr(err, "reading server response")

	if resp.StatusCode != 200 {
		log.Fatal(fmt.Sprintf("Failed to fetch fission v0.1 state: %v", string(body)))
	}
	return body
}

// track a name in the remapper, creating a new name if needed
func (nr *nameRemapper) TrackName(old string) {
	// all kubernetes names must match this regex
	kubeNameRegex := "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	maxLen := 63

	ok, err := regexp.MatchString(kubeNameRegex, old)
	CheckErr(err, "match name regexp")
	if ok && len(old) < maxLen {
		// no rename
		nr.oldToNew[old] = old
		nr.newNames[old] = true
		return
	}

	newName := strings.ToLower(old)

	// remove disallowed
	inv, err := regexp.Compile("[^-a-z0-9]")
	CheckErr(err, "compile regexp")
	newName = string(inv.ReplaceAll([]byte(newName), []byte("-")))

	// trim leading non-alphabetic
	leadingnonalpha, err := regexp.Compile("^[^a-z]+")
	CheckErr(err, "compile regexp")
	newName = string(leadingnonalpha.ReplaceAll([]byte(newName), []byte{}))

	// trim trailing
	trailing, err := regexp.Compile("[^a-z0-9]+$")
	CheckErr(err, "compile regexp")
	newName = string(trailing.ReplaceAll([]byte(newName), []byte{}))

	// truncate to length
	if len(newName) > maxLen-4 {
		newName = newName[0:(maxLen - 4)]
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

func UpgradeDumpV1State(v1url string, filename string) {
	var v1state V1FissionState

	fmt.Println("Getting environments")
	resp := Get(v1url + "/environments")
	err := json.Unmarshal(resp, &v1state.Environments)
	CheckErr(err, "parse server response")

	fmt.Println("Getting watches")
	resp = Get(v1url + "/watches")
	err = json.Unmarshal(resp, &v1state.Watches)
	CheckErr(err, "parse server response")

	fmt.Println("Getting routes")
	resp = Get(v1url + "/triggers/http")
	err = json.Unmarshal(resp, &v1state.HTTPTriggers)
	CheckErr(err, "parse server response")

	fmt.Println("Getting message queue triggers")
	resp = Get(v1url + "/triggers/messagequeue")
	err = json.Unmarshal(resp, &v1state.Mqtriggers)
	CheckErr(err, "parse server response")

	fmt.Println("Getting time triggers")
	resp = Get(v1url + "/triggers/time")
	err = json.Unmarshal(resp, &v1state.TimeTriggers)
	CheckErr(err, "parse server response")

	fmt.Println("Getting function list")
	resp = Get(v1url + "/functions")
	err = json.Unmarshal(resp, &v1state.Functions)
	CheckErr(err, "parse server response")

	// we have to change names that are disallowed in kubernetes
	nr := nameRemapper{
		oldToNew: make(map[string]string),
		newNames: make(map[string]bool),
	}

	// get all referenced function metadata
	funcMetaSet := make(map[v1.Metadata]bool)
	for _, f := range v1state.Functions {
		funcMetaSet[f.Metadata] = true
		nr.TrackName(f.Metadata.Name)
	}
	for _, t := range v1state.HTTPTriggers {
		funcMetaSet[t.Function] = true
		nr.TrackName(t.Metadata.Name)
	}
	for _, t := range v1state.Mqtriggers {
		funcMetaSet[t.Function] = true
		nr.TrackName(t.Metadata.Name)
	}
	for _, t := range v1state.Watches {
		funcMetaSet[t.Function] = true
		nr.TrackName(t.Metadata.Name)
	}
	for _, t := range v1state.TimeTriggers {
		funcMetaSet[t.Function] = true
		nr.TrackName(t.Metadata.Name)
	}

	for _, e := range v1state.Environments {
		nr.TrackName(e.Metadata.Name)
	}

	fmt.Println("Getting functions")
	// get each function
	funcs := make(map[v1.Metadata]v1.Function)
	for m := range funcMetaSet {
		if len(m.Uid) != 0 {
			resp = Get(fmt.Sprintf("%v/functions/%v?uid=%v", v1url, m.Name, m.Uid))
		} else {
			resp = Get(fmt.Sprintf("%v/functions/%v", v1url, m.Name))
		}

		var f v1.Function

		// unmarshal
		err = json.Unmarshal(resp, &f)
		CheckErr(err, "parse server response")

		// load into a map to remove duplicates
		funcs[f.Metadata] = f
	}

	// add list of unique functions to v1state from map
	v1state.Functions = make([]v1.Function, 0)
	for _, f := range funcs {
		v1state.Functions = append(v1state.Functions, f)
	}

	// dump name changes
	v1state.NameChanges = nr.oldToNew

	// serialize v1state
	out, err := json.MarshalIndent(v1state, "", "    ")
	CheckErr(err, "serialize v0.1 state")

	// dump to file fission-v01-state.json
	if len(filename) == 0 {
		filename = "fission-v01-state.json"
	}
	err = ioutil.WriteFile(filename, out, 0644)
	CheckErr(err, "write file")

	fmt.Printf("Done: Saved %v functions, %v HTTP triggers, %v watches, %v message queue triggers, %v time triggers.\n",
		len(v1state.Functions), len(v1state.HTTPTriggers), len(v1state.Watches), len(v1state.Mqtriggers), len(v1state.TimeTriggers))
}

func FunctionRefFromV1Metadata(m *v1.Metadata, nameRemap map[string]string) *fission.FunctionReference {
	return &fission.FunctionReference{
		Type: fission.FunctionReferenceTypeFunctionName,
		Name: nameRemap[m.Name],
	}
}

func CrdMetadataFromV1Metadata(m *v1.Metadata, nameRemap map[string]string) *metav1.ObjectMeta {
	return &metav1.ObjectMeta{
		Name:      nameRemap[m.Name],
		Namespace: metav1.NamespaceDefault,
	}
}
