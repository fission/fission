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
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dchest/uniuri"
	"github.com/satori/go.uuid"
	"github.com/urfave/cli"
	"k8s.io/client-go/1.5/pkg/api"

	"github.com/fission/fission"
	"github.com/fission/fission/fission/logdb"
	"github.com/fission/fission/tpr"
)

func fileSize(filePath string) int64 {
	info, err := os.Stat(filePath)
	checkErr(err, fmt.Sprintf("stat %v", filePath))
	return info.Size()
}

func getPackageFileName(srcPkgName string, deployPkgName string) (string, error) {
	if len(srcPkgName) == 0 && len(deployPkgName) == 0 {
		return "", errors.New("Need --code or --package to specify deployment package, or use --srcpkg to specify source package.")
	} else if len(srcPkgName) > 0 && len(deployPkgName) > 0 {
		return "", errors.New("Cannot specify deployment package (--code, --package) and source package (--srcpkg) at the same time.")
	} else if len(srcPkgName) == 0 && len(deployPkgName) > 0 {
		// deploy pkg is available
		return deployPkgName, nil
	} else {
		// source pkg is available
		return srcPkgName, nil
	}
}

func getPackageContents(filePath string) []byte {
	var code []byte
	var err error

	code, err = ioutil.ReadFile(filePath)
	checkErr(err, fmt.Sprintf("read %v", filePath))
	return code
}

func fnCreate(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	fnName := c.String("name")
	if len(fnName) == 0 {
		fatal("Need --name argument.")
	}

	envName := c.String("env")
	if len(envName) == 0 {
		fatal("Need --env argument.")
	}

	srcPkgName := c.String("srcpkg")

	deployPkgName := c.String("code")
	if len(deployPkgName) == 0 {
		deployPkgName = c.String("package")
	}

	fileName, err := getPackageFileName(srcPkgName, deployPkgName)
	if err != nil {
		fatal(err.Error())
	}

	if fileSize(fileName) > fission.PackageLiteralSizeLimit {
		// TODO fallback to uploading + setting a Package URL
		fmt.Printf("File size not supported yet")
		os.Exit(1)
	}

	pkgContents := getPackageContents(fileName)
	pkgName := fmt.Sprintf("%v-%v", fnName, strings.ToLower(uniuri.NewLen(6)))
	pkg := &tpr.Package{
		Metadata: api.ObjectMeta{
			Name:      pkgName,
			Namespace: api.NamespaceDefault,
		},
		Spec: fission.PackageSpec{
			Type:    fission.PackageTypeLiteral,
			Literal: pkgContents,
		},
	}
	_, err = client.PackageCreate(pkg)
	checkErr(err, "upload package")

	fnPkgRef := fission.FunctionPackageRef{
		PackageRef: fission.PackageRef{
			Name:      pkgName,
			Namespace: api.NamespaceDefault,
		},
	}

	function := &tpr.Function{
		Metadata: api.ObjectMeta{
			Name:      fnName,
			Namespace: api.NamespaceDefault,
		},
		Spec: fission.FunctionSpec{
			EnvironmentName: envName,
		},
	}

	if len(srcPkgName) > 0 {
		function.Spec.Source = fnPkgRef
	} else {
		function.Spec.Deployment = fnPkgRef
	}

	_, err = client.FunctionCreate(function)
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
	ht := &tpr.Httptrigger{
		Metadata: api.ObjectMeta{
			Name:      triggerName,
			Namespace: api.NamespaceDefault,
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
	m := &api.ObjectMeta{
		Name:      fnName,
		Namespace: api.NamespaceDefault,
	}
	fn, err := client.FunctionGet(m)
	checkErr(err, "get function")

	pkg, err := client.PackageGet(&api.ObjectMeta{
		Name:      fn.Spec.Deployment.PackageRef.Name,
		Namespace: fn.Spec.Deployment.PackageRef.Namespace,
	})
	checkErr(err, "get package")

	os.Stdout.Write(pkg.Spec.Literal)
	return err
}

func fnGetMeta(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	fnName := c.String("name")
	if len(fnName) == 0 {
		fatal("Need name of function, use --name")
	}

	m := &api.ObjectMeta{
		Name:      fnName,
		Namespace: api.NamespaceDefault,
	}

	f, err := client.FunctionGet(m)
	checkErr(err, "get function")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\n", "NAME", "UID", "ENV")
	fmt.Fprintf(w, "%v\t%v\t%v\n",
		f.Metadata.Name, f.Metadata.UID, f.Spec.EnvironmentName)
	w.Flush()
	return err
}

func fnUpdate(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	fnName := c.String("name")
	if len(fnName) == 0 {
		fatal("Need name of function, use --name")
	}

	function, err := client.FunctionGet(&api.ObjectMeta{
		Name:      fnName,
		Namespace: api.NamespaceDefault,
	})
	checkErr(err, fmt.Sprintf("read function '%v'", fnName))

	var pkgName string

	if len(function.Spec.Source.PackageRef.Name) > 0 {
		pkgName = function.Spec.Source.PackageRef.Name
	} else {
		pkgName = function.Spec.Deployment.PackageRef.Name
	}

	pkg, err := client.PackageGet(&api.ObjectMeta{
		Name:      pkgName,
		Namespace: api.NamespaceDefault,
	})
	checkErr(err, fmt.Sprintf("read package '%v'", pkgName))

	envName := c.String("env")
	deployPkgName := c.String("code")
	if len(deployPkgName) == 0 {
		deployPkgName = c.String("package")
	}
	srcPkgName := c.String("srcpkg")

	if len(envName) == 0 && len(deployPkgName) == 0 && len(srcPkgName) == 0 {
		fatal("Need --env or --code or --package or --srcpkg argument.")
	}

	fileName, err := getPackageFileName(srcPkgName, deployPkgName)
	if err != nil {
		fatal(err.Error())
	}

	if len(fileName) > 0 {
		if fileSize(fileName) > fission.PackageLiteralSizeLimit {
			// TODO fallback to uploading + setting a Package URL
			fmt.Printf("File size >256k not supported yet")
			os.Exit(1)
		}

		pkg.Spec.Literal = getPackageContents(fileName)

		_, err = client.PackageUpdate(pkg)
		checkErr(err, "update package")
	}

	if len(envName) > 0 {
		function.Spec.EnvironmentName = envName

		_, err = client.FunctionUpdate(function)
		checkErr(err, "update function")
	}

	fmt.Printf("function '%v' updated\n", fnName)
	return err
}

func fnDelete(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	fnName := c.String("name")
	if len(fnName) == 0 {
		fatal("Need name of function, use --name")
	}

	m := &api.ObjectMeta{
		Name:      fnName,
		Namespace: api.NamespaceDefault,
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
			f.Metadata.Name, f.Metadata.UID, f.Spec.EnvironmentName)
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
	m := &api.ObjectMeta{
		Name:      fnName,
		Namespace: api.NamespaceDefault,
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

	m := &api.ObjectMeta{Name: fnName}

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
