/*
Copyright 2019 The Fission Authors.

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

package support

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg/errors"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/support/resources"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/utils"
)

const (
	DUMP_ARCHIVE_PREFIX = "fission-dump"
	DEFAULT_OUTPUT_DIR  = "fission-dump"
)

type DumpSubCommand struct {
	cmd.CommandActioner
}

func Dump(input cli.Input) error {
	return (&DumpSubCommand{}).do(input)
}

func (opts *DumpSubCommand) do(input cli.Input) error {
	fmt.Println("Start dumping process...")

	nozip := input.Bool(flagkey.SupportNoZip)
	outputDir := input.String(flagkey.SupportOutput)
	// check whether the dump directory exists.
	_, err := os.Stat(outputDir)
	if err != nil && os.IsNotExist(err) {
		err = os.Mkdir(outputDir, 0755)
		if err != nil {
			panic(err)
		}
	} else if err != nil {
		panic(errors.Wrap(err, "Error checking dump directory status"))
	}

	outputDir, err = filepath.Abs(outputDir)
	if err != nil {
		panic(errors.Wrap(err, "Error creating dump directory for dumping files"))
	}

	k8sClient := opts.Client().KubernetesClient

	ress := map[string]resources.Resource{
		// kubernetes info
		"kubernetes-version": resources.NewKubernetesVersion(k8sClient),
		"kubernetes-nodes":   resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesNode, ""),

		// fission info
		"fission-version": resources.NewFissionVersion(opts.Client(), input),

		// fission component logs & spec
		"fission-components-svc-spec": resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesService,
			"svc in (buildermgr, executor, influxdb, kubewatcher, logger, mqtrigger, router, storagesvc, timer)"),
		"fission-components-deployment-spec": resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesDeployment,
			"svc in (buildermgr, executor, influxdb, kubewatcher, logger, mqtrigger, router, storagesvc, timer)"),
		"fission-components-daemonset-spec": resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesDaemonSet,
			"svc in (buildermgr, executor, influxdb, kubewatcher, logger, mqtrigger, router, storagesvc, timer)"),
		"fission-components-pod-spec": resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesPod,
			"svc in (buildermgr, executor, influxdb, kubewatcher, logger, mqtrigger, router, storagesvc, timer)"),
		"fission-components-pod-log": resources.NewKubernetesPodLogDumper(k8sClient,
			"svc in (buildermgr, executor, influxdb, kubewatcher, logger, mqtrigger, router, storagesvc, timer)"),

		// fission builder logs & spec
		"fission-builder-svc-spec":        resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesService, "owner=buildermgr"),
		"fission-builder-deployment-spec": resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesDeployment, "owner=buildermgr"),
		"fission-builder-pod-spec":        resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesPod, "owner=buildermgr"),
		"fission-builder-pod-log":         resources.NewKubernetesPodLogDumper(k8sClient, "owner=buildermgr"),

		// fission function logs & spec
		"fission-function-svc-spec":        resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesService, "executorType=newdeploy"),
		"fission-function-deployment-spec": resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesDeployment, "executorType in (poolmgr, newdeploy)"),
		"fission-function-pod-spec":        resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesPod, "executorType in (poolmgr, newdeploy)"),
		"fission-function-pod-log":         resources.NewKubernetesPodLogDumper(k8sClient, "executorType in (poolmgr, newdeploy)"),

		// CRD resources
		"fission-crd-packages":      resources.NewCrdDumper(opts.Client(), resources.CrdPackage),
		"fission-crd-environments":  resources.NewCrdDumper(opts.Client(), resources.CrdEnvironment),
		"fission-crd-functions":     resources.NewCrdDumper(opts.Client(), resources.CrdFunction),
		"fission-crd-httptriggers":  resources.NewCrdDumper(opts.Client(), resources.CrdHttpTrigger),
		"fission-crd-kubewatchers":  resources.NewCrdDumper(opts.Client(), resources.CrdKubeWatcher),
		"fission-crd-mqtriggers":    resources.NewCrdDumper(opts.Client(), resources.CrdMessageQueueTrigger),
		"fission-crd-timetriggers":  resources.NewCrdDumper(opts.Client(), resources.CrdTimeTrigger),
		"fission-crd-canaryconfigs": resources.NewCrdDumper(opts.Client(), resources.CrdCanaryConfig),
	}

	dumpName := fmt.Sprintf("%v_%v", DUMP_ARCHIVE_PREFIX, time.Now().Unix())
	dumpDir := filepath.Join(outputDir, dumpName)

	wg := &sync.WaitGroup{}

	tempDir, err := utils.GetTempDir()
	if err != nil {
		fmt.Printf("Error creating temporary directory: %v\n", err.Error())
		return err
	}

	for key, res := range ress {
		dir := fmt.Sprintf("%v/%v/", tempDir, key)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			err = os.MkdirAll(dir, 0755)
			if err != nil {
				panic(err)
			}
		}
		wg.Add(1)
		go func(res resources.Resource, dir string) {
			defer wg.Done()
			res.Dump(input.Context(), dir)
		}(res, dir)
	}

	wg.Wait()

	if !nozip {
		defer os.RemoveAll(tempDir)
		path := filepath.Join(outputDir, fmt.Sprintf("%v.zip", dumpName))
		_, err := utils.MakeZipArchiveWithGlobs(input.Context(), path, tempDir)
		if err != nil {
			fmt.Printf("Error creating archive for dump files: %v", err)
			return err
		}
		fmt.Printf("The archive dump file is %v\n", path)
	} else {
		err = os.Rename(tempDir, dumpDir)
		if err != nil {
			fmt.Printf("Error creating dump directory: %v\n", err.Error())
			return err
		}
		fmt.Printf("The dump files are placed at %v\n", dumpDir)
	}

	return nil
}
