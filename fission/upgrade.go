package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/dchest/uniuri"
	"github.com/urfave/cli"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission"
	"github.com/fission/fission/tpr"
	"github.com/fission/fission/v1"
)

type (
	V1FissionState struct {
		Functions    []v1.Function            `json:"functions"`
		Environments []v1.Environment         `json:"environments"`
		Httptriggers []v1.HTTPTrigger         `json:"httptriggers"`
		Mqtriggers   []v1.MessageQueueTrigger `json:"mqtriggers"`
		Timetriggers []v1.TimeTrigger         `json:"timetriggers"`
		Watches      []v1.Watch               `json:"watches"`
		NameChanges  map[string]string        `json:"namechanges"`
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
	kubeNameRegex := "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	maxLen := 63

	ok, err := regexp.MatchString(kubeNameRegex, old)
	checkErr(err, "match name regexp")
	if ok && len(old) < maxLen {
		// no rename
		nr.oldToNew[old] = old
		nr.newNames[old] = true
		return
	}

	newName := strings.ToLower(old)

	// remove disallowed
	inv, err := regexp.Compile("[^-a-z0-9]")
	checkErr(err, "compile regexp")
	newName = string(inv.ReplaceAll([]byte(newName), []byte("-")))

	// trim leading non-alphabetic
	leadingnonalpha, err := regexp.Compile("^[^a-z]+")
	checkErr(err, "compile regexp")
	newName = string(leadingnonalpha.ReplaceAll([]byte(newName), []byte{}))

	// trim trailing
	trailing, err := regexp.Compile("[^a-z0-9]+$")
	checkErr(err, "compile regexp")
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

func upgradeDumpV1State(v1url string, filename string) {
	var v1state V1FissionState

	fmt.Println("Getting environments")
	resp := get(v1url + "/environments")
	err := json.Unmarshal(resp, &v1state.Environments)
	checkErr(err, "parse server response")

	fmt.Println("Getting watches")
	resp = get(v1url + "/watches")
	err = json.Unmarshal(resp, &v1state.Watches)
	checkErr(err, "parse server response")

	fmt.Println("Getting routes")
	resp = get(v1url + "/triggers/http")
	err = json.Unmarshal(resp, &v1state.Httptriggers)
	checkErr(err, "parse server response")

	fmt.Println("Getting message queue triggers")
	resp = get(v1url + "/triggers/messagequeue")
	err = json.Unmarshal(resp, &v1state.Mqtriggers)
	checkErr(err, "parse server response")

	fmt.Println("Getting time triggers")
	resp = get(v1url + "/triggers/time")
	err = json.Unmarshal(resp, &v1state.Timetriggers)
	checkErr(err, "parse server response")

	fmt.Println("Getting function list")
	resp = get(v1url + "/functions")
	err = json.Unmarshal(resp, &v1state.Functions)
	checkErr(err, "parse server response")

	// we have to change names that are disallowed in kubernetes
	nr := nameRemapper{
		oldToNew: make(map[string]string),
		newNames: make(map[string]bool),
	}

	// get all referenced function metadata
	funcMetaSet := make(map[v1.Metadata]bool)
	for _, f := range v1state.Functions {
		funcMetaSet[f.Metadata] = true
		nr.trackName(f.Metadata.Name)
	}
	for _, t := range v1state.Httptriggers {
		funcMetaSet[t.Function] = true
		nr.trackName(t.Metadata.Name)
	}
	for _, t := range v1state.Mqtriggers {
		funcMetaSet[t.Function] = true
		nr.trackName(t.Metadata.Name)
	}
	for _, t := range v1state.Watches {
		funcMetaSet[t.Function] = true
		nr.trackName(t.Metadata.Name)
	}
	for _, t := range v1state.Timetriggers {
		funcMetaSet[t.Function] = true
		nr.trackName(t.Metadata.Name)
	}

	for _, e := range v1state.Environments {
		nr.trackName(e.Metadata.Name)
	}

	fmt.Println("Getting functions")
	// get each function
	funcs := make(map[v1.Metadata]v1.Function)
	for m := range funcMetaSet {
		if len(m.Uid) != 0 {
			resp = get(fmt.Sprintf("%v/functions/%v?uid=%v", v1url, m.Name, m.Uid))
		} else {
			resp = get(fmt.Sprintf("%v/functions/%v", v1url, m.Name))
		}

		var f v1.Function

		// unmarshal
		err = json.Unmarshal(resp, &f)
		checkErr(err, "parse server response")

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
	checkErr(err, "serialize v0.1 state")

	// dump to file fission-v01-state.json
	if len(filename) == 0 {
		filename = "fission-v01-state.json"
	}
	err = ioutil.WriteFile(filename, out, 0644)
	checkErr(err, "write file")

	fmt.Printf("Done: Saved %v functions, %v HTTP triggers, %v watches, %v message queue triggers, %v time triggers.\n",
		len(v1state.Functions), len(v1state.Httptriggers), len(v1state.Watches), len(v1state.Mqtriggers), len(v1state.Timetriggers))
}

func functionRefFromV1Metadata(m *v1.Metadata, nameRemap map[string]string) *fission.FunctionReference {
	return &fission.FunctionReference{
		Type: fission.FunctionReferenceTypeFunctionName,
		Name: nameRemap[m.Name],
	}
}

func tprMetadataFromV1Metadata(m *v1.Metadata, nameRemap map[string]string) *metav1.ObjectMeta {
	return &metav1.ObjectMeta{
		Name:      nameRemap[m.Name],
		Namespace: metav1.NamespaceDefault,
	}
}

func upgradeDumpState(c *cli.Context) error {
	u := getV1URL(c.GlobalString("server"))
	filename := c.String("file")

	// check v1
	resp, err := http.Get(u + "/environments")
	checkErr(err, "reach fission server")
	if resp.StatusCode == 404 {
		msg := fmt.Sprintf("Server %v isn't a v1 Fission server. Use --server to point at a pre-0.2.x Fission server.", u)
		fatal(msg)
	}

	upgradeDumpV1State(u, filename)
	return nil
}

func upgradeRestoreState(c *cli.Context) error {
	filename := c.String("file")
	if len(filename) == 0 {
		filename = "fission-v01-state.json"
	}

	contents, err := ioutil.ReadFile(filename)
	checkErr(err, fmt.Sprintf("open file %v", filename))

	var v1state V1FissionState
	err = json.Unmarshal(contents, &v1state)
	checkErr(err, "parse dumped v1 state")

	// create a regular v2 client
	client := getClient(c.GlobalString("server"))

	// create functions
	for _, f := range v1state.Functions {

		// get post-rename function name, derive pkg name from it
		fnName := v1state.NameChanges[f.Metadata.Name]
		pkgName := fmt.Sprintf("%v-%v", fnName, strings.ToLower(uniuri.NewLen(6)))

		// write function to file
		tmpfile, err := ioutil.TempFile("", pkgName)
		checkErr(err, "create temporary file")
		code, err := base64.StdEncoding.DecodeString(f.Code)
		checkErr(err, "decode base64 function contents")
		tmpfile.Write(code)
		tmpfile.Sync()
		tmpfile.Close()

		// upload
		archive := createArchive(client, tmpfile.Name())
		os.Remove(tmpfile.Name())

		// create pkg
		pkgSpec := fission.PackageSpec{
			Environment: fission.EnvironmentReference{
				Name:      v1state.NameChanges[f.Environment.Name],
				Namespace: metav1.NamespaceDefault,
			},
			Deployment: *archive,
		}
		pkg, err := client.PackageCreate(&tpr.Package{
			Metadata: metav1.ObjectMeta{
				Name:      pkgName,
				Namespace: metav1.NamespaceDefault,
			},
			Spec: pkgSpec,
		})
		checkErr(err, fmt.Sprintf("create package %v", pkgName))
		_, err = client.FunctionCreate(&tpr.Function{
			Metadata: *tprMetadataFromV1Metadata(&f.Metadata, v1state.NameChanges),
			Spec: fission.FunctionSpec{
				Environment: pkgSpec.Environment,
				Package: fission.FunctionPackageRef{
					PackageRef: fission.PackageRef{
						Name:            pkg.Name,
						Namespace:       pkg.Namespace,
						ResourceVersion: pkg.ResourceVersion,
					},
				},
			},
		})
		checkErr(err, fmt.Sprintf("create function %v", v1state.NameChanges[f.Metadata.Name]))

	}

	// create envs
	for _, e := range v1state.Environments {
		_, err = client.EnvironmentCreate(&tpr.Environment{
			Metadata: *tprMetadataFromV1Metadata(&e.Metadata, v1state.NameChanges),
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
	for _, t := range v1state.Httptriggers {
		_, err = client.HTTPTriggerCreate(&tpr.Httptrigger{
			Metadata: *tprMetadataFromV1Metadata(&t.Metadata, v1state.NameChanges),
			Spec: fission.HTTPTriggerSpec{
				RelativeURL:       t.UrlPattern,
				Method:            t.Method,
				FunctionReference: *functionRefFromV1Metadata(&t.Function, v1state.NameChanges),
			},
		})
		checkErr(err, fmt.Sprintf("create http trigger %v", t.Metadata.Name))
	}

	// create mqtriggers
	for _, t := range v1state.Mqtriggers {
		_, err = client.MessageQueueTriggerCreate(&tpr.Messagequeuetrigger{
			Metadata: *tprMetadataFromV1Metadata(&t.Metadata, v1state.NameChanges),
			Spec: fission.MessageQueueTriggerSpec{
				FunctionReference: *functionRefFromV1Metadata(&t.Function, v1state.NameChanges),
				MessageQueueType:  t.MessageQueueType,
				Topic:             t.Topic,
				ResponseTopic:     t.ResponseTopic,
			},
		})
		checkErr(err, fmt.Sprintf("create http trigger %v", t.Metadata.Name))
	}

	// create time triggers
	for _, t := range v1state.Timetriggers {
		_, err = client.TimeTriggerCreate(&tpr.Timetrigger{
			Metadata: *tprMetadataFromV1Metadata(&t.Metadata, v1state.NameChanges),
			Spec: fission.TimeTriggerSpec{
				FunctionReference: *functionRefFromV1Metadata(&t.Function, v1state.NameChanges),
				Cron:              t.Cron,
			},
		})
		checkErr(err, fmt.Sprintf("create time trigger %v", t.Metadata.Name))
	}

	// create watches
	for _, t := range v1state.Watches {
		_, err = client.WatchCreate(&tpr.Kuberneteswatchtrigger{
			Metadata: *tprMetadataFromV1Metadata(&t.Metadata, v1state.NameChanges),
			Spec: fission.KubernetesWatchTriggerSpec{
				Namespace:         t.Namespace,
				Type:              t.ObjType,
				FunctionReference: *functionRefFromV1Metadata(&t.Function, v1state.NameChanges),
			},
		})
		checkErr(err, fmt.Sprintf("create kubernetes watch trigger %v", t.Metadata.Name))
	}

	return nil
}
