// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

// ListSubCommand struct
type ListSubCommand struct {
	cmd.CommandActioner
}

// List lists resources in the spec.
func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) error {
	return opts.run(input)
}

func (opts *ListSubCommand) run(input cli.Input) error {
	deployID := input.String(flagkey.SpecDeployID)
	if len(deployID) == 0 {
		// get specdir, specignore and read the deployID
		specDir := util.GetSpecDir(input)
		specIgnore := util.GetSpecIgnore(input)
		fr, err := ReadSpecs(specDir, specIgnore, false)
		if err != nil {
			return fmt.Errorf("error reading specs: %w", err)
		}
		deployID = fr.DeploymentConfig.UID
	}

	_, currentNS, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fv1.AggregateValidationErrors("Environment", err)
	}

	if input.Bool(flagkey.AllNamespaces) {
		currentNS = metav1.NamespaceAll
	}

	return opts.getResource(input, currentNS, deployID)

}

func (opts *ListSubCommand) getResource(input cli.Input, namespace string, deployID string) (err error) {
	var allfn []fv1.Function
	printNS := namespace
	if printNS == metav1.NamespaceAll {
		printNS = "all"
	}

	allfn, err = getAllFunctions(input.Context(), opts.Client(), namespace)
	if err != nil {
		return fmt.Errorf("error getting Functions from %s namespaces: %w", printNS, err)
	}
	specfns := filterByDeployID[fv1.Function](allfn, deployID)
	ShowFunctions(specfns)

	var allenvs []fv1.Environment
	allenvs, err = getAllEnvironments(input.Context(), opts.Client(), namespace)
	if err != nil {
		return fmt.Errorf("error getting Environments from %s namespaces: %w", printNS, err)
	}
	specenvs := filterByDeployID[fv1.Environment](allenvs, deployID)
	ShowEnvironments(specenvs)

	var pkglists []fv1.Package
	pkglists, err = getAllPackages(input.Context(), opts.Client(), namespace)
	if err != nil {
		return fmt.Errorf("error getting Packages from %s namespaces: %w", printNS, err)
	}
	specPkgs := filterByDeployID[fv1.Package](pkglists, deployID)
	ShowPackages(specPkgs)

	var canaryCfgs []fv1.CanaryConfig
	canaryCfgs, err = getAllCanaryConfigs(input.Context(), opts.Client(), namespace)
	if err != nil {
		return fmt.Errorf("error getting Canary Config from %s namespaces: %w", printNS, err)
	}
	specCanaryCfgs := filterByDeployID[fv1.CanaryConfig](canaryCfgs, deployID)
	ShowCanaryConfigs(specCanaryCfgs)

	var hts []fv1.HTTPTrigger
	hts, err = getAllHTTPTriggers(input.Context(), opts.Client(), namespace)
	if err != nil {
		return fmt.Errorf("error getting HTTP Triggers from %s namespaces: %w", printNS, err)
	}
	specHTTPTriggers := filterByDeployID[fv1.HTTPTrigger](hts, deployID)
	ShowHTTPTriggers(specHTTPTriggers)

	var mqts []fv1.MessageQueueTrigger
	mqts, err = getAllMessageQueueTriggers(input.Context(), opts.Client(), input.String(flagkey.MqtMQType), namespace)
	if err != nil {
		return fmt.Errorf("error getting MessageQueue Triggers from %s namespaces: %w", printNS, err)
	}
	specMessageQueueTriggers := filterByDeployID[fv1.MessageQueueTrigger](mqts, deployID)
	ShowMQTriggers(specMessageQueueTriggers)

	var tts []fv1.TimeTrigger
	tts, err = getAllTimeTriggers(input.Context(), opts.Client(), namespace)
	if err != nil {
		return fmt.Errorf("error getting Time Triggers from %s namespaces: %w", printNS, err)
	}
	specTimeTriggers := filterByDeployID[fv1.TimeTrigger](tts, deployID)
	ShowTimeTriggers(specTimeTriggers)

	var wfs []fv1.Workflow
	wfs, err = getAllWorkflows(input.Context(), opts.Client(), namespace)
	if err != nil {
		return fmt.Errorf("error getting Workflows from %s namespaces: %w", printNS, err)
	}
	specWorkflows := filterByDeployID[fv1.Workflow](wfs, deployID)
	ShowWorkflows(specWorkflows)

	var kws []fv1.KubernetesWatchTrigger
	kws, err = getAllKubeWatchTriggers(input.Context(), opts.Client(), namespace)
	if err != nil {
		return fmt.Errorf("error getting Kube Watchers from %s namespaces: %w", printNS, err)
	}
	specKubeWatchers := filterByDeployID[fv1.KubernetesWatchTrigger](kws, deployID)
	ShowAppliedKubeWatchers(specKubeWatchers)

	var aliases []fv1.FunctionAlias
	aliases, err = getAllFunctionAliases(input.Context(), opts.Client(), namespace)
	if err != nil {
		return fmt.Errorf("error getting FunctionAliases from %s namespaces: %w", printNS, err)
	}
	specAliases := filterByDeployID[fv1.FunctionAlias](aliases, deployID)
	ShowFunctionAliases(specAliases)

	return err
}

// filterByDeployID returns the items annotated with the given deployment UID,
// i.e. the resources created by this spec deployment.
func filterByDeployID[T any, PT Object[T]](items []T, deployID string) []T {
	var out []T
	for i := range items {
		if PT(&items[i]).GetAnnotations()[FISSION_DEPLOYMENT_UID_KEY] == deployID {
			out = append(out, items[i])
		}
	}
	return out
}

// ShowFunctions displays info of Functions
func ShowFunctions(fns []fv1.Function) {
	if len(fns) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
		fmt.Fprintf(w, "%v\n", "Functions:")
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "ENV", "EXECUTORTYPE", "MINSCALE", "MAXSCALE", "MINCPU", "MAXCPU", "MINMEMORY", "MAXMEMORY", "SECRETS", "CONFIGMAPS")

		for _, f := range fns {
			secrets := f.Spec.Secrets
			configMaps := f.Spec.ConfigMaps
			var secretsList, configMapList []string
			for _, secret := range secrets {
				secretsList = append(secretsList, secret.Name)
			}
			for _, configMap := range configMaps {
				configMapList = append(configMapList, configMap.Name)
			}

			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
				f.Name, f.Spec.Environment.Name,
				f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType,
				f.Spec.InvokeStrategy.ExecutionStrategy.MinScale,
				f.Spec.InvokeStrategy.ExecutionStrategy.MaxScale,
				f.Spec.Resources.Requests.Cpu().String(),
				f.Spec.Resources.Limits.Cpu().String(),
				f.Spec.Resources.Requests.Memory().String(),
				f.Spec.Resources.Limits.Memory().String(),
				strings.Join(secretsList, ","),
				strings.Join(configMapList, ","))
		}
		fmt.Fprintf(w, "\n")
		w.Flush()
	}
}

// ShowEnvironments displays info of Environments
func ShowEnvironments(envs []fv1.Environment) {
	if len(envs) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
		fmt.Fprintf(w, "%v\n", "Environments:")
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "IMAGE", "BUILDER_IMAGE", "POOLSIZE", "MINCPU", "MAXCPU", "MINMEMORY", "MAXMEMORY", "EXTNET", "GRACETIME")
		for _, env := range envs {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
				env.Name, env.Spec.Runtime.Image, env.Spec.Builder.Image, env.Spec.Poolsize,
				env.Spec.Resources.Requests.Cpu(), env.Spec.Resources.Limits.Cpu(),
				env.Spec.Resources.Requests.Memory(), env.Spec.Resources.Limits.Memory(),
				env.Spec.AllowAccessToExternalNetwork, env.Spec.TerminationGracePeriod)
		}
		fmt.Fprintf(w, "\n")
		w.Flush()
	}
}

// ShowPackages displays info of Packages
func ShowPackages(pkgList []fv1.Package) {
	if len(pkgList) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
		fmt.Fprintf(w, "%v\n", "Packages:")
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", "NAME", "BUILD_STATUS", "ENV", "LASTUPDATEDAT")
		for _, pkg := range pkgList {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", pkg.Name, pkg.Status.BuildStatus, pkg.Spec.Environment.Name, pkg.Status.LastUpdateTimestamp.Format(time.RFC822))
		}
		fmt.Fprintf(w, "\n")
		w.Flush()
	}
}

// ShowCanaryConfigs displays info of Canary Configs
func ShowCanaryConfigs(canaryCfgs []fv1.CanaryConfig) {
	if len(canaryCfgs) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
		fmt.Fprintf(w, "%v\n", "Canary Config:")
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "TRIGGER", "FUNCTION-N", "FUNCTION-N-1", "WEIGHT-INCREMENT", "INTERVAL", "FAILURE-THRESHOLD", "FAILURE-TYPE", "STATUS")
		for _, canaryCfg := range canaryCfgs {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
				canaryCfg.Name, canaryCfg.Spec.Trigger, canaryCfg.Spec.NewFunction, canaryCfg.Spec.OldFunction, canaryCfg.Spec.WeightIncrement, canaryCfg.Spec.WeightIncrementDuration,
				canaryCfg.Spec.FailureThreshold, canaryCfg.Spec.FailureType, canaryCfg.Status.Status)
		}
		fmt.Fprintf(w, "\n")
		w.Flush()
	}
}

// ShowHTTPTriggers displays info of HTTP Triggers
func ShowHTTPTriggers(hts []fv1.HTTPTrigger) {
	if len(hts) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
		fmt.Fprintf(w, "%v\n", "HTTP Triggers:")
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "METHOD", "URL", "FUNCTION(s)", "INGRESS", "HOST", "PATH", "TLS", "ANNOTATIONS")
		for _, trigger := range hts {
			function := ""
			if trigger.Spec.FunctionReference.Type == fv1.FunctionReferenceTypeFunctionName {
				function = trigger.Spec.FunctionReference.Name
			} else {
				for k, v := range trigger.Spec.FunctionReference.FunctionWeights {
					function += fmt.Sprintf("%s:%v ", k, v)
				}
			}

			host := trigger.Spec.Host
			if len(trigger.Spec.IngressConfig.Host) > 0 {
				host = trigger.Spec.IngressConfig.Host
			}
			path := trigger.Spec.RelativeURL
			if trigger.Spec.Prefix != nil && *trigger.Spec.Prefix != "" {
				path = *trigger.Spec.Prefix
			}
			if len(trigger.Spec.IngressConfig.Path) > 0 {
				path = trigger.Spec.IngressConfig.Path
			}
			var msg []string
			for k, v := range trigger.Spec.IngressConfig.Annotations {
				msg = append(msg, fmt.Sprintf("%v: %v", k, v))
			}
			ann := strings.Join(msg, ", ")

			methods := []string{}
			if len(trigger.Spec.Method) > 0 {
				methods = append(methods, trigger.Spec.Method)
			}
			if len(trigger.Spec.Methods) > 0 {
				methods = trigger.Spec.Methods
			}

			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
				trigger.Name, methods, trigger.Spec.RelativeURL, function, trigger.Spec.CreateIngress, host, path, trigger.Spec.IngressConfig.TLS, ann)
		}
		fmt.Fprintf(w, "\n")
		w.Flush()
	}
}

// ShowMQTriggers displays info of MessageQueue Triggers
func ShowMQTriggers(mqts []fv1.MessageQueueTrigger) {
	if len(mqts) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
		fmt.Printf("\nMessageQueue Triggers:\n")
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "FUNCTION_NAME", "MESSAGE_QUEUE_TYPE", "TOPIC", "RESPONSE_TOPIC", "ERROR_TOPIC", "MAX_RETRIES", "PUB_MSG_CONTENT_TYPE")
		for _, mqt := range mqts {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
				mqt.Name, mqt.Spec.FunctionReference.Name, mqt.Spec.MessageQueueType, mqt.Spec.Topic, mqt.Spec.ResponseTopic, mqt.Spec.ErrorTopic, mqt.Spec.MaxRetries, mqt.Spec.ContentType)
		}
		fmt.Fprintf(w, "\n")
		w.Flush()
	}
}

// ShowTimeTriggers displays info of Time Triggers
func ShowTimeTriggers(tts []fv1.TimeTrigger) {
	if len(tts) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
		fmt.Fprintf(w, "%v", "Time Triggers:\n")
		fmt.Fprintf(w, "%v\t%v\t%v\n", "NAME", "CRON", "FUNCTION_NAME")
		for _, tt := range tts {

			fmt.Fprintf(w, "%v\t%v\t%v\n",
				tt.Name, tt.Spec.Cron, tt.Spec.Name)
		}
		fmt.Fprintf(w, "\n")
		w.Flush()
	}
}

// ShowWorkflows displays info of Workflows
func ShowWorkflows(wfs []fv1.Workflow) {
	if len(wfs) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
		fmt.Fprintf(w, "%v", "Workflows:\n")
		fmt.Fprintf(w, "%v\t%v\t%v\n", "NAME", "STARTAT", "STATES")
		for _, wf := range wfs {
			fmt.Fprintf(w, "%v\t%v\t%v\n",
				wf.Name, wf.Spec.StartAt, len(wf.Spec.States))
		}
		fmt.Fprintf(w, "\n")
		w.Flush()
	}
}

// ShowFunctionAliases displays info of FunctionAliases
func ShowFunctionAliases(aliases []fv1.FunctionAlias) {
	if len(aliases) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
		fmt.Fprintf(w, "%v", "FunctionAliases:\n")
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n", "NAME", "FUNCTION", "VERSION", "PACKAGEDIGEST", "RESOLVEDVERSION")
		for _, a := range aliases {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n",
				a.Name, a.Spec.FunctionName, a.Spec.Version, a.Spec.PackageDigest, a.Status.ResolvedVersion)
		}
		fmt.Fprintf(w, "\n")
		w.Flush()
	}
}

// ShowAppliedKubeWatchers displays info of kube watchers
func ShowAppliedKubeWatchers(ws []fv1.KubernetesWatchTrigger) {
	if len(ws) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
		fmt.Fprintf(w, "%v", "Kube Watchers:\n")
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n", "NAME", "NAMESPACE", "OBJTYPE", "LABELS", "FUNCTION_NAME")

		for _, wa := range ws {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n",
				wa.Name, wa.Spec.Namespace, wa.Spec.Type, wa.Spec.LabelSelector, wa.Spec.FunctionReference.Name)
		}
		fmt.Fprintf(w, "\n")
		w.Flush()
	}
}

// getAllFunctions get lists of functions in provided namespaces
func getAllFunctions(ctx context.Context, client cmd.Client, namespace string) ([]fv1.Function, error) {
	fns, err := client.FissionClientSet.CoreV1().Functions(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("unable to get Functions %w", err)
	}
	return fns.Items, nil
}

// getAllEnvironments get lists of environments in all namespaces
func getAllEnvironments(ctx context.Context, client cmd.Client, namespace string) ([]fv1.Environment, error) {
	envs, err := client.FissionClientSet.CoreV1().Environments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("unable to get Environments %w", err)
	}
	return envs.Items, nil
}

// getAllPackages get lists of packages in all namespaces
func getAllPackages(ctx context.Context, client cmd.Client, namespace string) ([]fv1.Package, error) {
	pkgList, err := client.FissionClientSet.CoreV1().Packages(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("unable to get Packages %w", err)
	}
	return pkgList.Items, nil
}

// getAllCanaryConfigs get lists of canary configs in all namespaces
func getAllCanaryConfigs(ctx context.Context, client cmd.Client, namespace string) ([]fv1.CanaryConfig, error) {
	canaryCfgs, err := client.FissionClientSet.CoreV1().CanaryConfigs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("unable to get Canary Configs %w", err)
	}
	return canaryCfgs.Items, nil
}

// getAllHTTPTriggers get lists of HTTP Triggers in all namespaces
func getAllHTTPTriggers(ctx context.Context, client cmd.Client, namespace string) ([]fv1.HTTPTrigger, error) {
	hts, err := client.FissionClientSet.CoreV1().HTTPTriggers(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("unable to get HTTP Triggers %w", err)
	}
	return hts.Items, nil
}

// getAllMessageQueueTriggers get lists of MessageQueue Triggers in all namespaces
func getAllMessageQueueTriggers(ctx context.Context, client cmd.Client, mqttype string, namespace string) ([]fv1.MessageQueueTrigger, error) {
	mqts, err := client.FissionClientSet.CoreV1().MessageQueueTriggers(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("unable to get MessageQueue Triggers %w", err)
	}
	return mqts.Items, nil
}

// getAllTimeTriggers get lists of Time Triggers in all namespaces
func getAllTimeTriggers(ctx context.Context, client cmd.Client, namespace string) ([]fv1.TimeTrigger, error) {
	tts, err := client.FissionClientSet.CoreV1().TimeTriggers(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("unable to get Time Triggers %w", err)
	}
	return tts.Items, nil
}

// getAllWorkflows get lists of Workflows in all namespaces
func getAllWorkflows(ctx context.Context, client cmd.Client, namespace string) ([]fv1.Workflow, error) {
	wfs, err := client.FissionClientSet.CoreV1().Workflows(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("unable to get Workflows %w", err)
	}
	return wfs.Items, nil
}

// getAllKubeWatchTriggers get lists of Kube Watchers in all namespaces
func getAllKubeWatchTriggers(ctx context.Context, client cmd.Client, namespace string) ([]fv1.KubernetesWatchTrigger, error) {
	ws, err := client.FissionClientSet.CoreV1().KubernetesWatchTriggers(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("unable to get Kube Watchers %w", err)
	}
	return ws.Items, nil
}

// getAllFunctionAliases get lists of FunctionAliases in all namespaces
func getAllFunctionAliases(ctx context.Context, client cmd.Client, namespace string) ([]fv1.FunctionAlias, error) {
	aliases, err := client.FissionClientSet.CoreV1().FunctionAliases(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("unable to get FunctionAliases %w", err)
	}
	return aliases.Items, nil
}
