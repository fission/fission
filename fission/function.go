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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dchest/uniuri"
	"github.com/satori/go.uuid"
	"github.com/urfave/cli"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission"
	"github.com/fission/fission/controller/client"
	"github.com/fission/fission/crd"
	"github.com/fission/fission/fission/logdb"
	storageSvcClient "github.com/fission/fission/storagesvc/client"
)

func fileSize(filePath string) int64 {
	info, err := os.Stat(filePath)
	checkErr(err, fmt.Sprintf("stat %v", filePath))
	return info.Size()
}

// upload a file and return a fission.Archive
func createArchive(client *client.Client, fileName string) *fission.Archive {
	var archive fission.Archive
	if fileSize(fileName) < fission.ArchiveLiteralSizeLimit {
		contents := getContents(fileName)
		archive.Type = fission.ArchiveTypeLiteral
		archive.Literal = contents
	} else {
		u := strings.TrimSuffix(client.Url, "/") + "/proxy/storage"
		ssClient := storageSvcClient.MakeClient(u)

		// TODO add a progress bar
		id, err := ssClient.Upload(fileName, nil)
		checkErr(err, fmt.Sprintf("upload file %v", fileName))

		archiveUrl := ssClient.GetUrl(id)

		archive.Type = fission.ArchiveTypeUrl
		archive.URL = archiveUrl

		f, err := os.Open(fileName)
		if err != nil {
			checkErr(err, fmt.Sprintf("find file %v", fileName))
		}
		defer f.Close()

		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			checkErr(err, fmt.Sprintf("calculate checksum for file %v", fileName))
		}

		archive.Checksum = fission.Checksum{
			Type: fission.ChecksumTypeSHA256,
			Sum:  hex.EncodeToString(h.Sum(nil)),
		}
	}
	return &archive
}

func createPackage(client *client.Client, fnName, envName, srcArchiveName, deployArchiveName, buildcmd string) *metav1.ObjectMeta {
	pkgSpec := fission.PackageSpec{
		Environment: fission.EnvironmentReference{
			Namespace: metav1.NamespaceDefault,
			Name:      envName,
		},
	}

	var pkgStatus fission.BuildStatus

	if len(deployArchiveName) > 0 {
		pkgSpec.Deployment = *createArchive(client, deployArchiveName)
	}
	if len(srcArchiveName) > 0 {
		pkgSpec.Source = *createArchive(client, srcArchiveName)
	}

	// start a build only when a package has no deploy archive
	if len(srcArchiveName) > 0 && len(deployArchiveName) == 0 {
		pkgStatus = fission.BuildStatusPending
	} else {
		pkgStatus = fission.BuildStatusNone
	}

	if len(buildcmd) > 0 {
		pkgSpec.BuildCommand = buildcmd
	}

	fnList, err := json.Marshal([]string{fnName})
	checkErr(err, "encode json")

	annotation := map[string]string{
		"createdForFunction": fnName,
		"usedByFunctions":    string(fnList),
	}

	pkgName := strings.ToLower(fmt.Sprintf("%v-%v", fnName, uniuri.NewLen(6)))
	pkg := &crd.Package{
		Metadata: metav1.ObjectMeta{
			Name:        pkgName,
			Namespace:   metav1.NamespaceDefault,
			Annotations: annotation,
		},
		Spec: pkgSpec,
		Status: fission.PackageStatus{
			BuildStatus: pkgStatus,
		},
	}
	pkgMetadata, err := client.PackageCreate(pkg)
	checkErr(err, "create package")
	return pkgMetadata
}

func getContents(filePath string) []byte {
	var code []byte
	var err error

	code, err = ioutil.ReadFile(filePath)
	checkErr(err, fmt.Sprintf("read %v", filePath))
	return code
}

func printPodLogs(c *cli.Context) error {
	fnName := c.String("name")
	if len(fnName) == 0 {
		fatal("Need --name argument.")
	}

	queryURL, err := url.Parse(c.GlobalString("server"))
	checkErr(err, "parse the base URL")
	queryURL.Path = fmt.Sprintf("/proxy/logs/%s", fnName)

	req, err := http.NewRequest("POST", queryURL.String(), nil)
	checkErr(err, "create logs request")

	httpClient := http.Client{}
	resp, err := httpClient.Do(req)
	checkErr(err, "execute get logs request")

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("get logs from pod directly")
	}

	body, err := ioutil.ReadAll(resp.Body)
	checkErr(err, "read the response body")
	fmt.Println(string(body))
	return nil
}

func fnCreate(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	if len(c.String("package")) > 0 {
		fatal("--package is deprecated, please use --deploy instead.")
	}

	fnName := c.String("name")
	if len(fnName) == 0 {
		fatal("Need --name argument.")
	}

	envName := c.String("env")
	if len(envName) == 0 {
		fatal("Need --env argument.")
	}

	secretName := c.String("secret")
	cfgMapName := c.String("configmap")
	
	secretNameSpace := c.String("secretns")
	cfgMapNameSpace := c.String("configmapns")

	if len(secretNameSpace) == 0 {
		secretNameSpace = metav1.NamespaceDefault
	}

	if len(cfgMapNameSpace) == 0 {
		cfgMapNameSpace = metav1.NamespaceDefault
	}

	fmt.Println("secret: %s cfg: %s", secretName, cfgMapName)

	srcArchiveName := c.String("src")
	deployArchiveName := c.String("code")
	if len(deployArchiveName) == 0 {
		deployArchiveName = c.String("deploy")
	}

	if len(srcArchiveName) == 0 && len(deployArchiveName) == 0 {
		fatal("Need --code or --deploy to specify deployment archive, or use --src to specify source archive.")
	}

	entrypoint := c.String("entrypoint")
	buildcmd := c.String("buildcmd")

	pkgMetadata := createPackage(client, fnName, envName, srcArchiveName, deployArchiveName, buildcmd)

	function := &crd.Function{
		Metadata: metav1.ObjectMeta{
			Name:      fnName,
			Namespace: metav1.NamespaceDefault,
		},
		Spec: fission.FunctionSpec{
			Environment: fission.EnvironmentReference{
				Name:      envName,
				Namespace: metav1.NamespaceDefault,
			},
			Package: fission.FunctionPackageRef{
				FunctionName: entrypoint,
				PackageRef: fission.PackageRef{
					Namespace:       pkgMetadata.Namespace,
					Name:            pkgMetadata.Name,
					ResourceVersion: pkgMetadata.ResourceVersion,
				},
			},

			SecretList: []fission.SecretReference{
				{Name:      secretName,
				Namespace:  secretNameSpace,},
			},

			ConfigMapList: []fission.ConfigMapReference{
				{Name:      cfgMapName,
				Namespace:  cfgMapNameSpace,},
			},
		},
	}

	_, err := client.FunctionCreate(function)
	checkErr(err, "create function")

	fmt.Printf("function '%v' created\n", fnName)

	// Allow the user to specify an HTTP trigger while creating a function.
	triggerUrl := c.String("url")
	if len(triggerUrl) == 0 {
		return nil
	}
	method := c.String("method")
	if len(method) == 0 {
		method = "GET"
	}
	triggerName := uuid.NewV4().String()
	ht := &crd.HTTPTrigger{
		Metadata: metav1.ObjectMeta{
			Name:      triggerName,
			Namespace: metav1.NamespaceDefault,
		},
		Spec: fission.HTTPTriggerSpec{
			RelativeURL: triggerUrl,
			Method:      getMethod(method),
			FunctionReference: fission.FunctionReference{
				Type: fission.FunctionReferenceTypeFunctionName,
				Name: fnName,
			},
		},
	}
	_, err = client.HTTPTriggerCreate(ht)
	checkErr(err, "create HTTP trigger")
	fmt.Printf("route created: %v %v -> %v\n", method, triggerUrl, fnName)

	return err
}

func fnGet(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	fnName := c.String("name")
	if len(fnName) == 0 {
		fatal("Need name of function, use --name")
	}
	m := &metav1.ObjectMeta{
		Name:      fnName,
		Namespace: metav1.NamespaceDefault,
	}
	fn, err := client.FunctionGet(m)
	checkErr(err, "get function")

	pkg, err := client.PackageGet(&metav1.ObjectMeta{
		Name:      fn.Spec.Package.PackageRef.Name,
		Namespace: fn.Spec.Package.PackageRef.Namespace,
	})
	checkErr(err, "get package")

	os.Stdout.Write(pkg.Spec.Deployment.Literal)
	return err
}

func fnGetMeta(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	fnName := c.String("name")
	if len(fnName) == 0 {
		fatal("Need name of function, use --name")
	}

	m := &metav1.ObjectMeta{
		Name:      fnName,
		Namespace: metav1.NamespaceDefault,
	}

	f, err := client.FunctionGet(m)
	checkErr(err, "get function")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\n", "NAME", "UID", "ENV")
	fmt.Fprintf(w, "%v\t%v\t%v\n",
		f.Metadata.Name, f.Metadata.UID, f.Spec.Environment.Name)
	w.Flush()
	return err
}

func fnUpdate(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	fnName := c.String("name")
	if len(fnName) == 0 {
		fatal("Need name of function, use --name")
	}

	if len(c.String("package")) > 0 {
		fatal("--package is deprecated, please use --deploy instead.")
	}

	function, err := client.FunctionGet(&metav1.ObjectMeta{
		Name:      fnName,
		Namespace: metav1.NamespaceDefault,
	})
	checkErr(err, fmt.Sprintf("read function '%v'", fnName))

	envName := c.String("env")
	deployArchiveName := c.String("code")
	if len(deployArchiveName) == 0 {
		deployArchiveName = c.String("deploy")
	}
	srcArchiveName := c.String("src")

	if len(envName) == 0 && len(deployArchiveName) == 0 && len(srcArchiveName) == 0 {
		fatal("Need --env or --code or --package or --deploy argument.")
	}

	if len(envName) > 0 {
		function.Spec.Environment.Name = envName
	}

	entrypoint := c.String("entrypoint")
	if len(entrypoint) > 0 {
		function.Spec.Package.FunctionName = entrypoint
	}

	secretName := c.String("secret")
	secretNameSpace := c.String("secretns")

	//require user to input both secret and secretnamespace together
	if len(secretName) == 0 || len(secretNameSpace) == 0 {
		if len(secretNameSpace) > 0 || len(secretName) > 0 {
			fatal("Need both secret and secret namespace for update")

		}
	}
	secretExists := false
	if len(secretName) > 0  {

		for i := range function.Spec.SecretList {
			if function.Spec.SecretList[i].Name == secretName{
				//update secretNamespace
				function.Spec.SecretList[i].Namespace = secretNameSpace
				secretExists = true
			}
			break
		}

		if !secretExists {
			
			newSecret := fission.SecretReference{
				Name:       secretName,
				Namespace:  secretNameSpace,
			}
			function.Spec.SecretList = append(function.Spec.SecretList, newSecret)
		}
	}

	cfgMapName := c.String("configmap")
	cfgMapNameSpace := c.String("configmapns")

	//require user to input both configmap and configmap namespace together
	if len(cfgMapName) == 0 || len(cfgMapNameSpace) == 0 {
		if len(cfgMapNameSpace) > 0 || len(cfgMapName) > 0 {
			fatal("Need both configmap and cfgmap namespace for update")

		}
	}

	cfgMapExists := false
	if len(cfgMapName) > 0  {

		for i := range function.Spec.ConfigMapList {
			if function.Spec.ConfigMapList[i].Name == cfgMapName{
				//update secretNamespace
				function.Spec.ConfigMapList[i].Namespace = cfgMapNameSpace
				cfgMapExists = true
			}
			break
		}

		if !cfgMapExists {
			
			newCfgMap := fission.ConfigMapReference{
				Name:       cfgMapName,
				Namespace:  cfgMapNameSpace,
			}
			function.Spec.ConfigMapList = append(function.Spec.ConfigMapList, newCfgMap)
		}
	}

	pkg, err := client.PackageGet(&metav1.ObjectMeta{
		Name:      function.Spec.Package.PackageRef.Name,
		Namespace: function.Spec.Package.PackageRef.Namespace,
	})
	checkErr(err, fmt.Sprintf("read package '%v'", function.Spec.Package.PackageRef.Name))

	buildcmd := c.String("buildcmd")
	if len(buildcmd) == 0 {
		// use previous build command if not specified.
		buildcmd = pkg.Spec.BuildCommand
	}

	if len(deployArchiveName) > 0 || len(srcArchiveName) > 0 {
		// create a new package for function
		pkgMetadata := createPackage(client, function.Metadata.Name,
			function.Spec.Environment.Name, srcArchiveName, deployArchiveName, buildcmd)

		// update function spec with resource version
		function.Spec.Package.PackageRef = fission.PackageRef{
			Namespace:       pkgMetadata.Namespace,
			Name:            pkgMetadata.Name,
			ResourceVersion: pkgMetadata.ResourceVersion,
		}
	}

	_, err = client.FunctionUpdate(function)
	checkErr(err, "update function")

	fmt.Printf("function '%v' updated\n", fnName)
	return err
}

func fnDelete(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	fnName := c.String("name")
	if len(fnName) == 0 {
		fatal("Need name of function, use --name")
	}

	m := &metav1.ObjectMeta{
		Name:      fnName,
		Namespace: metav1.NamespaceDefault,
	}

	err := client.FunctionDelete(m)
	checkErr(err, fmt.Sprintf("delete function '%v'", fnName))

	fmt.Printf("function '%v' deleted\n", fnName)
	return err
}

func fnList(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	fns, err := client.FunctionList()
	checkErr(err, "list functions")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)

	fmt.Fprintf(w, "%v\t%v\t%v\n", "NAME", "UID", "ENV")
	for _, f := range fns {
		fmt.Fprintf(w, "%v\t%v\t%v\n",
			f.Metadata.Name, f.Metadata.UID, f.Spec.Environment.Name)
	}
	w.Flush()

	return err
}

func fnLogs(c *cli.Context) error {

	client := getClient(c.GlobalString("server"))

	fnName := c.String("name")
	if len(fnName) == 0 {
		fatal("Need name of function, use --name")
	}

	dbType := c.String("dbtype")
	if len(dbType) == 0 {
		dbType = logdb.INFLUXDB
	}

	fnPod := c.String("pod")
	m := &metav1.ObjectMeta{
		Name:      fnName,
		Namespace: metav1.NamespaceDefault,
	}

	f, err := client.FunctionGet(m)
	checkErr(err, "get function")

	// request the controller to establish a proxy server to the database.
	logDB, err := logdb.GetLogDB(dbType, c.GlobalString("server"))
	if err != nil {
		fatal("failed to connect log database")
	}

	requestChan := make(chan struct{})
	responseChan := make(chan struct{})
	ctx := context.Background()

	go func(ctx context.Context, requestChan, responseChan chan struct{}) {
		t := time.Unix(0, 0*int64(time.Millisecond))
		for {
			select {
			case <-requestChan:
				logFilter := logdb.LogFilter{
					Pod:      fnPod,
					Function: f.Metadata.Name,
					FuncUid:  string(f.Metadata.UID),
					Since:    t,
				}
				logEntries, err := logDB.GetLogs(logFilter)
				if err != nil {
					fatal("failed to query logs")
				}
				for _, logEntry := range logEntries {
					if c.Bool("d") {
						fmt.Printf("Timestamp: %s\nNamespace: %s\nFunction Name: %s\nFunction ID: %s\nPod: %s\nContainer: %s\nStream: %s\nLog: %s\n---\n",
							logEntry.Timestamp, logEntry.Namespace, logEntry.FuncName, logEntry.FuncUid, logEntry.Pod, logEntry.Container, logEntry.Stream, logEntry.Message)
					} else {
						fmt.Printf("[%s] %s\n", logEntry.Timestamp, logEntry.Message)
					}
					t = logEntry.Timestamp
				}
				responseChan <- struct{}{}
			case <-ctx.Done():
				return
			}
		}
	}(ctx, requestChan, responseChan)

	for {
		requestChan <- struct{}{}
		<-responseChan
		if !c.Bool("f") {
			ctx.Done()
			return nil
		}
		time.Sleep(1 * time.Second)
	}
}

func fnPods(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	fnName := c.String("name")
	if len(fnName) == 0 {
		fatal("Need name of function, use --name")
	}

	dbType := c.String("dbtype")
	if len(dbType) == 0 {
		dbType = logdb.INFLUXDB
	}

	m := &metav1.ObjectMeta{Name: fnName}

	f, err := client.FunctionGet(m)
	checkErr(err, "get function")

	// client first sends db query to the controller, then the controller
	// will establish a proxy server that bridges the client and the database.
	logDB, err := logdb.GetLogDB(dbType, c.GlobalString("server"))
	if err != nil {
		fatal("failed to connect log database")
	}

	logFilter := logdb.LogFilter{
		Function: f.Metadata.Name,
		FuncUid:  string(f.Metadata.UID),
	}
	pods, err := logDB.GetPods(logFilter)
	if err != nil {
		fatal("failed to get pods of function")
		return err
	}
	fmt.Printf("NAME\t\n")
	for _, pod := range pods {
		fmt.Println(pod)
	}

	return err
}

func fnTest(c *cli.Context) error {
	fnName := c.String("name")
	routerURL := os.Getenv("FISSION_ROUTER")
	if len(routerURL) == 0 {
		fatal("Need FISSION_ROUTER set to your fission router.")
	}

	url := fmt.Sprintf("http://%s/fission-function/%s", routerURL, fnName)

	resp := httpRequest(c.String("method"), url, c.String("body"), c.StringSlice("header"))
	if resp.StatusCode < 400 {
		body, err := ioutil.ReadAll(resp.Body)
		checkErr(err, "Function test")
		fmt.Print(string(body))
		defer resp.Body.Close()
		return nil
	}

	body, err := ioutil.ReadAll(resp.Body)
	checkErr(err, "read log response from pod")
	fmt.Printf("Error calling function %v: %v %v", fnName, resp.StatusCode, string(body))
	defer resp.Body.Close()
	err = printPodLogs(c)
	if err != nil {
		fnLogs(c)
	}

	return nil
}
