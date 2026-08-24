// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

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

// Label selectors for the workloads a support bundle collects. Hoisted into
// constants because each is used by three or four dumpers, and when the list
// was repeated inline it drifted: eight of the components the chart ships were
// missing from every bundle, mqt-keda among them.
const (
	// componentSelector matches the control-plane Deployments. Keep in sync
	// with the svc: labels in charts/fission-all/templates/*/deployment.yaml —
	// a component absent here has no spec and no logs in the bundle at all.
	componentSelector = "svc in (agentruntime, buildermgr, canaryconfig, executor, kubewatcher, mcp, mqtrigger, " +
		"mqtrigger-keda, router, router-internal, statestore, statesvc, storagesvc, tenantcontroller, timer, " +
		"webhook-service, workflow)"

	builderSelector = "owner=buildermgr"

	// functionSelector covers all three execution backends. container was
	// missing, so container-executor functions had no spec, pods or logs.
	functionSelector = "executorType in (poolmgr, newdeploy, container)"

	// functionSvcSelector is narrower: poolmgr serves from the shared pool and
	// creates no per-function Service, while newdeploy and container both do.
	functionSvcSelector = "executorType in (newdeploy, container)"
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
		// Dumps include cluster pod logs and CRD specs; restrict to the
		// invoking user. The dump tarball is intended for sharing with
		// support, but as files on disk these should not be world-readable.
		err = os.Mkdir(outputDir, 0o700)
		if err != nil {
			return fmt.Errorf("error creating dump directory %q: %w", outputDir, err)
		}
	} else if err != nil {
		return fmt.Errorf("error checking dump directory status: %w", err)
	}

	outputDir, err = filepath.Abs(outputDir)
	if err != nil {
		return fmt.Errorf("error resolving absolute dump directory path: %w", err)
	}

	k8sClient := opts.Client().KubernetesClient

	// Every pod-event dumper shares one index. The dumpers below run
	// concurrently, so a list each would mean three full-cluster Event LISTs in
	// flight at once.
	podEvents := resources.NewPodEventIndex(k8sClient)

	// Likewise for the Deployment list: the mqt-consumer dumpers select on owner
	// reference rather than a label, and the orphaned-event dumper needs the
	// same list to attribute a pod that no longer exists.
	deployments := resources.NewDeploymentIndex(k8sClient)

	ress := map[string]resources.Resource{
		// kubernetes info
		"kubernetes-version": resources.NewKubernetesVersion(k8sClient),
		"kubernetes-nodes":   resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesNode, ""),

		// fission info
		"fission-version": resources.NewFissionVersion(opts.Client(), input),

		// fission component logs & spec
		"fission-components-svc-spec": resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesService,
			componentSelector),
		"fission-components-deployment-spec": resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesDeployment,
			componentSelector),
		"fission-components-daemonset-spec": resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesDaemonSet,
			componentSelector),
		"fission-components-pod-spec": resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesPod,
			componentSelector),
		"fission-components-pod-log": resources.NewKubernetesPodLogDumper(k8sClient,
			componentSelector),
		"fission-components-pod-events": resources.NewKubernetesPodEventDumper(k8sClient,
			componentSelector, podEvents),

		// fission builder logs & spec
		"fission-builder-svc-spec":        resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesService, builderSelector),
		"fission-builder-deployment-spec": resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesDeployment, builderSelector),
		"fission-builder-pod-spec":        resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesPod, builderSelector),
		"fission-builder-pod-log":         resources.NewKubernetesPodLogDumper(k8sClient, builderSelector),
		"fission-builder-pod-events":      resources.NewKubernetesPodEventDumper(k8sClient, builderSelector, podEvents),

		// fission function logs & spec
		"fission-function-svc-spec":        resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesService, functionSvcSelector),
		"fission-function-deployment-spec": resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesDeployment, functionSelector),
		"fission-function-pod-spec":        resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesPod, functionSelector),
		"fission-function-pod-log":         resources.NewKubernetesPodLogDumper(k8sClient, functionSelector),
		"fission-function-pod-events":      resources.NewKubernetesPodEventDumper(k8sClient, functionSelector, podEvents),
		// The HPA is what decides whether a function scales at all, and the
		// dumper for it existed but was never registered.
		"fission-function-hpa": resources.NewKubernetesObjectDumper(k8sClient, resources.KubernetesHPA, functionSelector),

		// Events of fission pods that no longer exist — the OOMKilled or evicted
		// pod is usually the one being investigated.
		"fission-orphaned-pod-events": resources.NewOrphanedPodEventDumper(k8sClient, podEvents, deployments,
			componentSelector, builderSelector, functionSelector),

		// The workload a keda MessageQueueTrigger scales. Selected by owner
		// reference: the scaler labels it app=<mqt name> and nothing else.
		"fission-mqt-consumer-deployment-spec": resources.NewMqtConsumerDumper(k8sClient, deployments,
			resources.MqtConsumerDeployment),
		"fission-mqt-consumer-pod-spec": resources.NewMqtConsumerDumper(k8sClient, deployments,
			resources.MqtConsumerPod),
		"fission-mqt-consumer-pod-log": resources.NewMqtConsumerDumper(k8sClient, deployments,
			resources.MqtConsumerLog),

		// CRD resources
		"fission-crd-packages":      resources.NewCrdDumper(opts.Client(), resources.CrdPackage),
		"fission-crd-environments":  resources.NewCrdDumper(opts.Client(), resources.CrdEnvironment),
		"fission-crd-functions":     resources.NewCrdDumper(opts.Client(), resources.CrdFunction),
		"fission-crd-httptriggers":  resources.NewCrdDumper(opts.Client(), resources.CrdHttpTrigger),
		"fission-crd-kubewatchers":  resources.NewCrdDumper(opts.Client(), resources.CrdKubeWatcher),
		"fission-crd-mqtriggers":    resources.NewCrdDumper(opts.Client(), resources.CrdMessageQueueTrigger),
		"fission-crd-timetriggers":  resources.NewCrdDumper(opts.Client(), resources.CrdTimeTrigger),
		"fission-crd-canaryconfigs": resources.NewCrdDumper(opts.Client(), resources.CrdCanaryConfig),

		// KEDA objects created by the mqt-keda scaler for MessageQueueTriggers.
		// Skipped quietly when KEDA is not installed.
		"fission-keda-scaledobjects":          resources.NewKedaDumper(opts.Client(), resources.KedaScaledObject),
		"fission-keda-triggerauthentications": resources.NewKedaDumper(opts.Client(), resources.KedaTriggerAuthentication),
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
			err = os.MkdirAll(dir, 0o700)
			if err != nil {
				return fmt.Errorf("error creating dump subdirectory %q: %w", dir, err)
			}
		}
		wg.Go(func() {
			func(res resources.Resource, dir string) {
				res.Dump(input.Context(), dir)
			}(res, dir)
		})
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
