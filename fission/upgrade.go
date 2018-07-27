package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"

	"github.com/dchest/uniuri"
	"github.com/urfave/cli"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission"
	"github.com/fission/fission/crd"
	"github.com/fission/fission/fission/sdk"
)

func upgradeDumpState(c *cli.Context) error {
	u := sdk.GetV1URL(c.GlobalString("server"))
	filename := c.String("file")

	// check v1
	resp, err := http.Get(u + "/environments")
	if err != nil {
		return sdk.FailedToError(err, "reach fission server")
	}
	if resp.StatusCode == http.StatusNotFound {
		msg := fmt.Sprintf("Server %v isn't a v1 Fission server. Use --server to point at a pre-0.2.x Fission server.", u)
		return sdk.GeneralError(msg)
	}

	sdk.UpgradeDumpV1State(u, filename)
	return nil
}

func upgradeRestoreState(c *cli.Context) error {
	filename := c.String("file")
	if len(filename) == 0 {
		filename = "fission-v01-state.json"
	}

	contents, err := ioutil.ReadFile(filename)
	if err != nil {
		return sdk.FailedToError(err, fmt.Sprintf("open file %v", filename))
	}

	var v1state sdk.V1FissionState
	err = json.Unmarshal(contents, &v1state)
	if err != nil {
		return sdk.FailedToError(err, "parse dumped v1 state")
	}

	// create a regular v2 client
	client := sdk.GetClient(c.GlobalString("server"))

	// create functions
	for _, f := range v1state.Functions {

		// get post-rename function name, derive pkg name from it
		fnName := v1state.NameChanges[f.Metadata.Name]
		pkgName := fmt.Sprintf("%v-%v", fnName, strings.ToLower(uniuri.NewLen(6)))

		// write function to file
		tmpfile, err := ioutil.TempFile("", pkgName)
		if err != nil {
			return sdk.FailedToError(err, "create temporary file")
		}
		code, err := base64.StdEncoding.DecodeString(f.Code)
		if err != nil {
			return sdk.FailedToError(err, "decode base64 function contents")
		}
		tmpfile.Write(code)
		tmpfile.Sync()
		tmpfile.Close()

		// upload
		archive, err := sdk.CreateArchive(client, tmpfile.Name(), "")
		if err != nil {
			return sdk.FailedToError(err, fmt.Sprintf("create archive for function %s", fnName))
		}
		os.Remove(tmpfile.Name())

		// create pkg
		pkgSpec := fission.PackageSpec{
			Environment: fission.EnvironmentReference{
				Name:      v1state.NameChanges[f.Environment.Name],
				Namespace: metav1.NamespaceDefault,
			},
			Deployment: *archive,
		}
		pkg, err := client.PackageCreate(&crd.Package{
			Metadata: metav1.ObjectMeta{
				Name:      pkgName,
				Namespace: metav1.NamespaceDefault,
			},
			Spec: pkgSpec,
		})
		if err != nil {
			return sdk.FailedToError(err, fmt.Sprintf("create package %v", pkgName))
		}
		_, err = client.FunctionCreate(&crd.Function{
			Metadata: *sdk.CrdMetadataFromV1Metadata(&f.Metadata, v1state.NameChanges),
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
		if err != nil {
			return sdk.FailedToError(err, fmt.Sprintf("create function %v", v1state.NameChanges[f.Metadata.Name]))
		}

	}

	// create envs
	for _, e := range v1state.Environments {
		_, err = client.EnvironmentCreate(&crd.Environment{
			Metadata: *sdk.CrdMetadataFromV1Metadata(&e.Metadata, v1state.NameChanges),
			Spec: fission.EnvironmentSpec{
				Version: 1,
				Runtime: fission.Runtime{
					Image: e.RunContainerImageUrl,
				},
			},
		})
		if err != nil {
			return sdk.FailedToError(err, fmt.Sprintf("create environment %v", e.Metadata.Name))
		}
	}

	// create httptriggers
	for _, t := range v1state.HTTPTriggers {
		_, err = client.HTTPTriggerCreate(&crd.HTTPTrigger{
			Metadata: *sdk.CrdMetadataFromV1Metadata(&t.Metadata, v1state.NameChanges),
			Spec: fission.HTTPTriggerSpec{
				RelativeURL:       t.UrlPattern,
				Method:            t.Method,
				FunctionReference: *sdk.FunctionRefFromV1Metadata(&t.Function, v1state.NameChanges),
			},
		})
		if err != nil {
			return sdk.FailedToError(err, fmt.Sprintf("create http trigger %v", t.Metadata.Name))
		}
	}

	// create mqtriggers
	for _, t := range v1state.Mqtriggers {
		_, err = client.MessageQueueTriggerCreate(&crd.MessageQueueTrigger{
			Metadata: *sdk.CrdMetadataFromV1Metadata(&t.Metadata, v1state.NameChanges),
			Spec: fission.MessageQueueTriggerSpec{
				FunctionReference: *sdk.FunctionRefFromV1Metadata(&t.Function, v1state.NameChanges),
				MessageQueueType:  fission.MessageQueueTypeNats, // only NATS is supported at that time (v1 types)
				Topic:             t.Topic,
				ResponseTopic:     t.ResponseTopic,
			},
		})
		if err != nil {
			return sdk.FailedToError(err, fmt.Sprintf("create http trigger %v", t.Metadata.Name))
		}
	}

	// create time triggers
	for _, t := range v1state.TimeTriggers {
		_, err = client.TimeTriggerCreate(&crd.TimeTrigger{
			Metadata: *sdk.CrdMetadataFromV1Metadata(&t.Metadata, v1state.NameChanges),
			Spec: fission.TimeTriggerSpec{
				FunctionReference: *sdk.FunctionRefFromV1Metadata(&t.Function, v1state.NameChanges),
				Cron:              t.Cron,
			},
		})
		if err != nil {
			return sdk.FailedToError(err, fmt.Sprintf("create time trigger %v", t.Metadata.Name))
		}
	}

	// create watches
	for _, t := range v1state.Watches {
		_, err = client.WatchCreate(&crd.KubernetesWatchTrigger{
			Metadata: *sdk.CrdMetadataFromV1Metadata(&t.Metadata, v1state.NameChanges),
			Spec: fission.KubernetesWatchTriggerSpec{
				Namespace:         t.Namespace,
				Type:              t.ObjType,
				FunctionReference: *sdk.FunctionRefFromV1Metadata(&t.Function, v1state.NameChanges),
			},
		})
		if err != nil {
			return sdk.FailedToError(err, fmt.Sprintf("create kubernetes watch trigger %v", t.Metadata.Name))
		}
	}

	return nil
}
