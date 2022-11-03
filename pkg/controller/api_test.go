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

package controller

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	uuid "github.com/satori/go.uuid"
	"go.uber.org/zap"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/controller/client/rest"
	"github.com/fission/fission/pkg/crd"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

var (
	// g struct {
	// 	cmd.CommandActioner
	// }
	testNS = metav1.NamespaceDefault
)

func panicIf(err error) {
	if err != nil {
		log.Panicf("err: %v", err)
	}
}

func assert(c bool, msg string) {
	if !c {
		log.Fatalf("assert failed: %v", msg)
	}
}

func assertNameReuseFailure(err error, name string) {
	assert(err != nil, "recreating "+name+" with same name must fail")
	fe, ok := err.(ferror.Error)
	assert(ok, "error must be a fission Error")
	assert(fe.Code == ferror.ErrorNameExists, "error must be a name exists error")
}

func assertNotFoundFailure(err error, name string) {
	assert(err != nil, "requesting a non-existent "+name+" must fail")
	fe, ok := err.(ferror.Error)
	assert(ok, "error must be a fission Error")
	if fe.Code != ferror.ErrorNotFound {
		log.Fatalf("error must be a not found error: %v", fe)
	}
}

func assertCronSpecFails(err error) {
	assert(err != nil, "using an invalid cron spec must fail")
	ok := strings.Contains(err.Error(), "not a valid cron spec")
	assert(ok, "invalid cron spec must fail")
}

func TestFunctionApi(t *testing.T) {
	testFunc := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: testNS,
		},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{
				Name:      "nodejs",
				Namespace: testNS,
			},
			Package: fv1.FunctionPackageRef{
				FunctionName: "xxx",
				PackageRef: fv1.PackageRef{
					Namespace:       testNS,
					Name:            "xxx",
					ResourceVersion: "12345",
				},
			},
		},
	}

	restClient := rest.NewRESTClient("http://localhost:8888")
	dClient := client.MakeClientset(restClient)

	_, err := dClient.V1().Function().Get(&metav1.ObjectMeta{
		Name:      testFunc.ObjectMeta.Name,
		Namespace: testNS,
	})
	assertNotFoundFailure(err, "function")

	m, err := dClient.V1().Function().Create(testFunc)
	panicIf(err)
	defer func() {
		err := dClient.V1().Function().Delete(m)
		panicIf(err)
	}()

	_, err = dClient.V1().Function().Create(testFunc)
	assertNameReuseFailure(err, "function")

	testFunc.ObjectMeta.ResourceVersion = m.ResourceVersion
	testFunc.Spec.Package.FunctionName = "yyy"
	_, err = dClient.V1().Function().Update(testFunc)
	panicIf(err)

	testFunc.ObjectMeta.ResourceVersion = ""
	testFunc.ObjectMeta.Name = "bar"
	m2, err := dClient.V1().Function().Create(testFunc)
	panicIf(err)
	defer func() {
		err := dClient.V1().Function().Delete(m2)
		panicIf(err)
	}()

	funcs, err := dClient.V1().Function().List(testNS)
	panicIf(err)
	assert(len(funcs) == 2, fmt.Sprintf("created two functions, but found %v", len(funcs)))

	funcs_url := dClient.ServerURL() + "/v2/functions"
	resp, err := http.Get(funcs_url)
	panicIf(err)
	defer resp.Body.Close()
	assert(resp.StatusCode == 200, "http get status code on /v1/functions")

	var found bool = false
	for _, b := range resp.Header["Content-Type"] {
		if b == "application/json; charset=utf-8" {
			found = true
		}
	}
	assert(found, "incorrect response content type")
}

func TestHTTPTriggerApi(t *testing.T) {
	testTrigger := &fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: testNS,
		},
		Spec: fv1.HTTPTriggerSpec{
			Methods:     []string{http.MethodGet},
			RelativeURL: "/hello",
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "foo",
			},
		},
	}

	restClient := rest.NewRESTClient("http://localhost:8888")
	dClient := client.MakeClientset(restClient)

	_, err := dClient.V1().HTTPTrigger().Get(&metav1.ObjectMeta{
		Name:      testTrigger.ObjectMeta.Name,
		Namespace: testNS,
	})
	assertNotFoundFailure(err, "httptrigger")

	m, err := dClient.V1().HTTPTrigger().Create(testTrigger)
	panicIf(err)
	defer func() {
		err := dClient.V1().HTTPTrigger().Delete(m)
		panicIf(err)
	}()

	_, err = dClient.V1().HTTPTrigger().Create(testTrigger)
	assertNameReuseFailure(err, "httptrigger")

	tr, err := dClient.V1().HTTPTrigger().Get(m)
	panicIf(err)
	assert(len(testTrigger.Spec.Methods) == len(tr.Spec.Methods) &&
		testTrigger.Spec.RelativeURL == tr.Spec.RelativeURL &&
		testTrigger.Spec.FunctionReference.Type == tr.Spec.FunctionReference.Type &&
		testTrigger.Spec.FunctionReference.Name == tr.Spec.FunctionReference.Name, "trigger should match after reading")

	testTrigger.ObjectMeta.ResourceVersion = m.ResourceVersion
	testTrigger.Spec.RelativeURL = "/hi"
	_, err = dClient.V1().HTTPTrigger().Update(testTrigger)
	panicIf(err)

	testTrigger.ObjectMeta.ResourceVersion = ""
	testTrigger.ObjectMeta.Name = "yyy"
	_, err = dClient.V1().HTTPTrigger().Create(testTrigger)
	assert(err != nil, "duplicate trigger should not be allowed")

	testTrigger.Spec.RelativeURL = "/hi2"
	m2, err := dClient.V1().HTTPTrigger().Create(testTrigger)
	panicIf(err)
	defer func() {
		err = dClient.V1().HTTPTrigger().Delete(m2)
		panicIf(err)
	}()

	ts, err := dClient.V1().HTTPTrigger().List(testNS)
	panicIf(err)
	assert(len(ts) == 2, fmt.Sprintf("created two triggers, but found %v", len(ts)))
}

func createFissionFnForMultipleTrigger() {
	testFunc := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo1",
			Namespace: testNS,
		},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{
				Name:      "nodejs",
				Namespace: testNS,
			},
			Package: fv1.FunctionPackageRef{
				FunctionName: "xxx",
				PackageRef: fv1.PackageRef{
					Namespace:       testNS,
					Name:            "xxx",
					ResourceVersion: "12345",
				},
			},
		},
	}

	restClient := rest.NewRESTClient("http://localhost:8888")
	dClient := client.MakeClientset(restClient)
	_, err := dClient.V1().Function().Create(testFunc)
	panicIf(err)
	defer func() {
		panicIf(err)
	}()

	testFunc.Name = "foo2"

	_, err = dClient.V1().Function().Create(testFunc)
	panicIf(err)
	defer func() {
		panicIf(err)
	}()
}

func TestHTTPTriggerCreateMultipleTrigger(t *testing.T) {
	logger := loggerfactory.GetLogger()
	createFissionFnForMultipleTrigger()
	prefix := "url_new"
	testTrigger := &fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo1",
			Namespace: testNS,
		},

		Spec: fv1.HTTPTriggerSpec{
			Methods: []string{http.MethodGet},
			Prefix:  &prefix,
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "foo1",
			},
		},
	}

	restClient := rest.NewRESTClient("http://localhost:8888")
	dClient := client.MakeClientset(restClient)
	m, err := dClient.V1().HTTPTrigger().Create(testTrigger)
	panicIf(err)
	defer panicIf(dClient.V1().HTTPTrigger().Delete(m))

	prefix_2 := "url_another"
	testTrigger2 := &fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo2",
			Namespace: testNS,
		},

		Spec: fv1.HTTPTriggerSpec{
			Methods: []string{http.MethodGet},
			Prefix:  &prefix_2,
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "foo2",
			},
		},
	}

	m2, err := dClient.V1().HTTPTrigger().Create(testTrigger2)

	if err != nil {
		t.Fatal()
	}

	defer func() {
		if m2 != nil {
			err := dClient.V1().HTTPTrigger().Delete(m2)
			if err != nil {
				logger.Error("Error deleting http trigger", zap.String("name", m2.Name), zap.Error(err))
			}
		}
	}()

}

func TestEnvironmentApi(t *testing.T) {
	testEnv := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: testNS,
		},
		Spec: fv1.EnvironmentSpec{
			Version: 1,
			Runtime: fv1.Runtime{
				Image: "gcr.io/xyz",
			},
			Resources: v1.ResourceRequirements{},
		},
	}

	restClient := rest.NewRESTClient("http://localhost:8888")
	dClient := client.MakeClientset(restClient)

	_, err := dClient.V1().Environment().Get(&metav1.ObjectMeta{
		Name:      testEnv.ObjectMeta.Name,
		Namespace: testNS,
	})
	assertNotFoundFailure(err, "environment")

	m, err := dClient.V1().Environment().Create(testEnv)
	panicIf(err)
	defer func() {
		err := dClient.V1().Environment().Delete(m)
		panicIf(err)
	}()

	_, err = dClient.V1().Environment().Create(testEnv)
	assertNameReuseFailure(err, "environment")

	e, err := dClient.V1().Environment().Get(m)
	panicIf(err)
	assert(reflect.DeepEqual(testEnv.Spec, e.Spec), "env should match after reading")

	testEnv.ObjectMeta.ResourceVersion = m.ResourceVersion
	testEnv.Spec.Runtime.Image = "another-img"
	_, err = dClient.V1().Environment().Update(testEnv)
	panicIf(err)

	testEnv.ObjectMeta.ResourceVersion = ""
	testEnv.ObjectMeta.Name = "bar"

	m2, err := dClient.V1().Environment().Create(testEnv)
	panicIf(err)
	defer func() {
		err := dClient.V1().Environment().Delete(m2)
		panicIf(err)
	}()

	ts, err := dClient.V1().Environment().List(testNS)
	panicIf(err)
	assert(len(ts) == 2, fmt.Sprintf("created two envs, but found %v", len(ts)))
}

func TestWatchApi(t *testing.T) {
	testWatch := &fv1.KubernetesWatchTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "xxx",
			Namespace: testNS,
		},
		Spec: fv1.KubernetesWatchTriggerSpec{
			Namespace: "default",
			Type:      "pod",
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "foo",
			},
		},
	}

	restClient := rest.NewRESTClient("http://localhost:8888")
	dClient := client.MakeClientset(restClient)

	_, err := dClient.V1().KubeWatcher().Get(&metav1.ObjectMeta{
		Name:      testWatch.ObjectMeta.Name,
		Namespace: testNS,
	})
	assertNotFoundFailure(err, "watch")

	m, err := dClient.V1().KubeWatcher().Create(testWatch)
	panicIf(err)
	defer func() {
		err := dClient.V1().KubeWatcher().Delete(m)
		panicIf(err)
	}()

	_, err = dClient.V1().KubeWatcher().Create(testWatch)
	assertNameReuseFailure(err, "watch")

	w, err := dClient.V1().KubeWatcher().Get(m)
	panicIf(err)
	assert(testWatch.Spec.Namespace == w.Spec.Namespace &&
		testWatch.Spec.Type == w.Spec.Type &&
		testWatch.Spec.FunctionReference.Type == w.Spec.FunctionReference.Type &&
		testWatch.Spec.FunctionReference.Name == w.Spec.FunctionReference.Name, "watch should match after reading")

	testWatch.ObjectMeta.Name = "yyy"
	m2, err := dClient.V1().KubeWatcher().Create(testWatch)
	panicIf(err)
	defer func() {
		err := dClient.V1().KubeWatcher().Delete(m2)
		panicIf(err)
	}()

	ws, err := dClient.V1().KubeWatcher().List(testNS)
	panicIf(err)
	assert(len(ws) == 2, fmt.Sprintf("created two watches, but found %v", len(ws)))
}

func TestTimeTriggerApi(t *testing.T) {
	testTrigger := &fv1.TimeTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "xxx",
			Namespace: testNS,
		},
		Spec: fv1.TimeTriggerSpec{
			Cron: "0 30 * * * *",
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "asdf",
			},
		},
	}

	restClient := rest.NewRESTClient("http://localhost:8888")
	dClient := client.MakeClientset(restClient)

	_, err := dClient.V1().TimeTrigger().Get(&metav1.ObjectMeta{Name: testTrigger.ObjectMeta.Name})
	assertNotFoundFailure(err, "trigger")

	m, err := dClient.V1().TimeTrigger().Create(testTrigger)
	panicIf(err)
	defer func() {
		err := dClient.V1().TimeTrigger().Delete(m)
		panicIf(err)
	}()

	_, err = dClient.V1().TimeTrigger().Create(testTrigger)
	assertNameReuseFailure(err, "trigger")

	tr, err := dClient.V1().TimeTrigger().Get(m)
	panicIf(err)
	assert(testTrigger.Spec.Cron == tr.Spec.Cron &&
		testTrigger.Spec.FunctionReference.Type == tr.Spec.FunctionReference.Type &&
		testTrigger.Spec.FunctionReference.Name == tr.Spec.FunctionReference.Name, "trigger should match after reading")

	testTrigger.ObjectMeta.ResourceVersion = m.ResourceVersion
	testTrigger.Spec.Cron = "@hourly"
	_, err = dClient.V1().TimeTrigger().Update(testTrigger)
	panicIf(err)

	testTrigger.ObjectMeta.ResourceVersion = ""
	testTrigger.ObjectMeta.Name = "yyy"
	testTrigger.Spec.Cron = "Not valid cron spec"
	_, err = dClient.V1().TimeTrigger().Create(testTrigger)
	assertCronSpecFails(err)

	ts, err := dClient.V1().TimeTrigger().List(testNS)
	panicIf(err)
	assert(len(ts) == 1, fmt.Sprintf("created two time triggers, but found %v", len(ts)))
}

func TestMain(m *testing.M) {
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())

	logger := loggerfactory.GetLogger()

	// skip test if no cluster available for testing
	kubeconfig := os.Getenv("KUBECONFIG")
	if len(kubeconfig) == 0 {
		log.Println("Skipping test, no kubernetes cluster")
		cancel()
		return
	}

	_, kubeClient, _, _, err := crd.GetKubernetesClient()
	panicIf(err)

	// testNS isolation for running multiple CI builds concurrently.
	id, err := uuid.NewV4()
	panicIf(err)
	testNS = id.String()
	_, err = kubeClient.CoreV1().Namespaces().Create(ctx, &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNS,
		},
	}, metav1.CreateOptions{})
	panicIf(err)

	go Start(ctx, logger, 8888, true)

	time.Sleep(5 * time.Second)

	fissionClient, kubernetesClient, _, _, err := crd.MakeFissionClient()
	panicIf(err)
	// TODO: use fake rest client for offline spec generation
	cmd.SetClientset(fissionClient, kubernetesClient)

	resp, err := http.Get("http://localhost:8888/")
	panicIf(err)
	assert(resp.StatusCode == 200, "http get status code on root")

	var found bool = false
	for _, b := range resp.Header["Content-Type"] {
		if b == "application/json; charset=utf-8" {
			found = true
		}
	}
	assert(found, "incorrect response content type")

	_, err = io.ReadAll(resp.Body)
	panicIf(err)

	exitVal := m.Run()
	logger.Info("Deleting test namespace", zap.String("namespace", testNS))
	gracePeriod := int64(0)
	err = kubeClient.CoreV1().Namespaces().Delete(ctx, testNS, metav1.DeleteOptions{GracePeriodSeconds: &gracePeriod})
	if err != nil {
		logger.Error("error deleting test namespace", zap.String("namespace", testNS), zap.Error(err))
	}
	cancel()
	os.Exit(exitVal)
}
