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

package spec

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pkg/errors"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
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
	// get specdir
	specDir := util.GetSpecDir(input)

	// read everything from spec
	fr, err := ReadSpecs(specDir)
	if err != nil {
		return errors.Wrap(err, "error reading specs")
	}

	fns, err := opts.Client().V1().Function().List("")
	if err != nil {
		return errors.Wrap(err, "error listing functions")
	}

	flag := true
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	for _, f := range fns {
		if f.Metadata.Annotations["fission-uid"] == fr.DeploymentConfig.UID {
			if flag {
				fmt.Printf("\nFunctions:\n")
				fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "ENV", "EXECUTORTYPE", "MINSCALE", "MAXSCALE", "MINCPU", "MAXCPU", "MINMEMORY", "MAXMEMORY", "TARGETCPU", "SECRETS", "CONFIGMAPS")
				flag = false
			}
			secrets := f.Spec.Secrets
			configMaps := f.Spec.ConfigMaps
			var secretsList, configMapList []string
			for _, secret := range secrets {
				secretsList = append(secretsList, secret.Name)
			}
			for _, configMap := range configMaps {
				configMapList = append(configMapList, configMap.Name)
			}

			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
				f.Metadata.Name, f.Spec.Environment.Name,
				f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType,
				f.Spec.InvokeStrategy.ExecutionStrategy.MinScale,
				f.Spec.InvokeStrategy.ExecutionStrategy.MaxScale,
				f.Spec.Resources.Requests.Cpu().String(),
				f.Spec.Resources.Limits.Cpu().String(),
				f.Spec.Resources.Requests.Memory().String(),
				f.Spec.Resources.Limits.Memory().String(),
				f.Spec.InvokeStrategy.ExecutionStrategy.TargetCPUPercent,
				strings.Join(secretsList, ","),
				strings.Join(configMapList, ","))
		}
	}
	w.Flush()

	envs, err := opts.Client().V1().Environment().List("")
	if err != nil {
		return errors.Wrap(err, "error listing enviornments")
	}

	flag = true
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	for _, env := range envs {
		if env.Metadata.Annotations["fission-uid"] == fr.DeploymentConfig.UID {
			if flag {
				fmt.Printf("\nEnviornments:\n")
				fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "IMAGE", "BUILDER_IMAGE", "POOLSIZE", "MINCPU", "MAXCPU", "MINMEMORY", "MAXMEMORY", "EXTNET", "GRACETIME")
				flag = false
			}
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
				env.Metadata.Name, env.Spec.Runtime.Image, env.Spec.Builder.Image, env.Spec.Poolsize,
				env.Spec.Resources.Requests.Cpu(), env.Spec.Resources.Limits.Cpu(),
				env.Spec.Resources.Requests.Memory(), env.Spec.Resources.Limits.Memory(),
				env.Spec.AllowAccessToExternalNetwork, env.Spec.TerminationGracePeriod)
		}
	}
	w.Flush()

	pkgList, err := opts.Client().V1().Package().List("")
	if err != nil {
		return errors.Wrap(err, "error listing packages")
	}

	flag = true
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	for _, pkg := range pkgList {
		if pkg.Metadata.Annotations["fission-uid"] == fr.DeploymentConfig.UID {
			if flag {
				fmt.Printf("\nPackages:\n")
				fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", "NAME", "BUILD_STATUS", "ENV", "LASTUPDATEDAT")
				flag = false
			}
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", pkg.Metadata.Name, pkg.Status.BuildStatus, pkg.Spec.Environment.Name, pkg.Status.LastUpdateTimestamp.Format(time.RFC822))
		}
	}
	w.Flush()

	canaryCfgs, err := opts.Client().V1().CanaryConfig().List("")
	if err != nil {
		return errors.Wrap(err, "Error listing Canary Config")
	}

	flag = true
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	for _, canaryCfg := range canaryCfgs {
		if canaryCfg.Metadata.Annotations["fission-uid"] == fr.DeploymentConfig.UID {
			if flag {
				fmt.Printf("\nCanary Config:\n")
				fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "TRIGGER", "FUNCTION-N", "FUNCTION-N-1", "WEIGHT-INCREMENT", "INTERVAL", "FAILURE-THRESHOLD", "FAILURE-TYPE", "STATUS")
				flag = false
			}
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
				canaryCfg.Metadata.Name, canaryCfg.Spec.Trigger, canaryCfg.Spec.NewFunction, canaryCfg.Spec.OldFunction, canaryCfg.Spec.WeightIncrement, canaryCfg.Spec.WeightIncrementDuration,
				canaryCfg.Spec.FailureThreshold, canaryCfg.Spec.FailureType, canaryCfg.Status.Status)
		}
	}
	w.Flush()

	hts, err := opts.Client().V1().HTTPTrigger().List("")
	if err != nil {
		return errors.Wrap(err, "Error listing HTTPTrigger")
	}
	flag = true
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	for _, trigger := range hts {
		if trigger.Metadata.Annotations["fission-uid"] == fr.DeploymentConfig.UID {
			if flag {
				fmt.Printf("\nHttp Triggers:\n")
				fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "METHOD", "URL", "FUNCTION(s)", "INGRESS", "HOST", "PATH", "TLS", "ANNOTATIONS")
				flag = false
			}
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
			if len(trigger.Spec.IngressConfig.Path) > 0 {
				path = trigger.Spec.IngressConfig.Path
			}

			var msg []string
			for k, v := range trigger.Spec.IngressConfig.Annotations {
				msg = append(msg, fmt.Sprintf("%v: %v", k, v))
			}
			ann := strings.Join(msg, ", ")

			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
				trigger.Metadata.Name, trigger.Spec.Method, trigger.Spec.RelativeURL, function, trigger.Spec.CreateIngress, host, path, trigger.Spec.IngressConfig.TLS, ann)

		}
	}
	w.Flush()

	mqts, err := opts.Client().V1().MessageQueueTrigger().List(input.String(flagkey.MqtMQType), "")
	if err != nil {
		return errors.Wrap(err, "Error listing MessageQueueTrigger")
	}
	flag = true
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	for _, mqt := range mqts {
		if mqt.Metadata.Annotations["fission-uid"] == fr.DeploymentConfig.UID {
			if flag {
				fmt.Printf("\nMessageQueue Triggers:\n")
				fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
					"NAME", "FUNCTION_NAME", "MESSAGE_QUEUE_TYPE", "TOPIC", "RESPONSE_TOPIC", "ERROR_TOPIC", "MAX_RETRIES", "PUB_MSG_CONTENT_TYPE")
				flag = false
			}
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
				mqt.Metadata.Name, mqt.Spec.FunctionReference.Name, mqt.Spec.MessageQueueType, mqt.Spec.Topic, mqt.Spec.ResponseTopic, mqt.Spec.ErrorTopic, mqt.Spec.MaxRetries, mqt.Spec.ContentType)
		}
	}
	w.Flush()

	tts, err := opts.Client().V1().TimeTrigger().List("")
	if err != nil {
		return errors.Wrap(err, "Error listing TimeTrigger")
	}
	flag = true
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	for _, tt := range tts {
		if tt.Metadata.Annotations["fission-uid"] == fr.DeploymentConfig.UID {
			if flag {
				fmt.Printf("\nTime Triggers:\n")
				fmt.Fprintf(w, "%v\t%v\t%v\n", "NAME", "CRON", "FUNCTION_NAME")
				flag = false
			}
			fmt.Fprintf(w, "%v\t%v\t%v\n",
				tt.Metadata.Name, tt.Spec.Cron, tt.Spec.FunctionReference.Name)
		}
	}
	w.Flush()

	ws, err := opts.Client().V1().KubeWatcher().List("")
	if err != nil {
		return errors.Wrap(err, "Error listing kubewatches")
	}
	flag = true
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	for _, wa := range ws {
		if wa.Metadata.Annotations["fission-uid"] == fr.DeploymentConfig.UID {
			if flag {
				fmt.Printf("\nKube Watches:\n")
				fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n",
					"NAME", "NAMESPACE", "OBJTYPE", "LABELS", "FUNCTION_NAME")
				flag = false
			}
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n",
				wa.Metadata.Name, wa.Spec.Namespace, wa.Spec.Type, wa.Spec.LabelSelector, wa.Spec.FunctionReference.Name)
		}
	}
	w.Flush()

	return nil
}
