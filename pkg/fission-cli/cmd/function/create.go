// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"errors"
	"fmt"
	"os"

	asv2 "k8s.io/api/autoscaling/v2"
	apiv1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/executor/util/hpa"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/httptrigger"
	_package "github.com/fission/fission/pkg/fission-cli/cmd/package"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/utils/uuid"
)

const (
	DEFAULT_MIN_SCALE   = 1
	DEFAULT_CONCURRENCY = 500
)

type CreateSubCommand struct {
	cmd.CommandActioner
	function *fv1.Function
	specFile string
}

func Create(input cli.Input) error {
	return (&CreateSubCommand{}).do(input)
}

// getStreamingConfig builds a StreamingConfig from the --streaming* flags, or
// nil when --streaming is not set (the classic, non-streaming proxy path).
func getStreamingConfig(input cli.Input) *fv1.StreamingConfig {
	if !input.Bool(flagkey.FnStreaming) {
		return nil
	}
	return &fv1.StreamingConfig{
		Protocol:           fv1.StreamingProtocol(input.String(flagkey.FnStreamingProtocol)),
		IdleTimeoutSeconds: input.Int(flagkey.FnStreamingIdleTimeout),
		MaxDurationSeconds: input.Int(flagkey.FnStreamingMaxDuration),
	}
}

// getToolConfig builds a ToolConfig from the --expose-as-mcp / --tool-* flags,
// or nil when --expose-as-mcp is not set (the function is not advertised as an
// MCP tool). It merges onto existing (the function's current Tool, or nil on
// create), overwriting only the fields whose flag was explicitly set — so an
// `fn update --expose-as-mcp` that omits --tool-name/--tool-input-schema keeps
// the previously-set values instead of clearing them. The --tool-input-schema
// flag points at a JSON Schema file whose raw contents are stored verbatim.
func getToolConfig(input cli.Input, existing *fv1.ToolConfig) (*fv1.ToolConfig, error) {
	if !input.Bool(flagkey.FnExposeAsMCP) {
		return nil, nil
	}
	tc := &fv1.ToolConfig{}
	if existing != nil {
		tc = existing.DeepCopy()
	}
	if input.IsSet(flagkey.FnToolDescription) {
		tc.Description = input.String(flagkey.FnToolDescription)
	}
	if input.IsSet(flagkey.FnToolName) {
		tc.ToolName = input.String(flagkey.FnToolName)
	}
	if input.IsSet(flagkey.FnToolInputSchema) {
		path := input.String(flagkey.FnToolInputSchema)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading tool input schema file %q: %w", path, err)
		}
		tc.InputSchema = &apiextensionsv1.JSON{Raw: raw}
	}
	return tc, nil
}

// getInvocationConfig builds the RFC-0024 async InvocationConfig from the
// --async-* flags, merging onto existing (the function's current config, or nil on
// create) so an `fn update` that sets only one field keeps the rest. It returns nil
// when nothing is configured. An empty --async-on-success/--async-on-failure (or
// their -topic variants) clears that destination. Field bounds and the destination
// shape are validated server-side by the Function admission webhook, so the CLI
// stays thin.
func getInvocationConfig(input cli.Input, existing *fv1.InvocationConfig) (*fv1.InvocationConfig, error) {
	set := input.IsSet(flagkey.FnAsyncMaxAttempts) || input.IsSet(flagkey.FnAsyncMaxAge) ||
		input.IsSet(flagkey.FnAsyncOnSuccess) || input.IsSet(flagkey.FnAsyncOnFailure) ||
		input.IsSet(flagkey.FnAsyncOnSuccessTopic) || input.IsSet(flagkey.FnAsyncOnFailureTopic)
	if !set {
		return existing, nil
	}
	ic := &fv1.InvocationConfig{}
	if existing != nil {
		ic = existing.DeepCopy()
	}
	if input.IsSet(flagkey.FnAsyncMaxAttempts) {
		ic.Retry.MaxAttempts = new(input.Int(flagkey.FnAsyncMaxAttempts))
	}
	if input.IsSet(flagkey.FnAsyncMaxAge) {
		ic.MaxAge = &metav1.Duration{Duration: input.Duration(flagkey.FnAsyncMaxAge)}
	}
	var err error
	if ic.OnSuccess, err = destinationFromFlags(input, flagkey.FnAsyncOnSuccess, flagkey.FnAsyncOnSuccessTopic, ic.OnSuccess); err != nil {
		return nil, err
	}
	if ic.OnFailure, err = destinationFromFlags(input, flagkey.FnAsyncOnFailure, flagkey.FnAsyncOnFailureTopic, ic.OnFailure); err != nil {
		return nil, err
	}
	return ic, nil
}

// destinationFromFlags resolves one destination condition from its flag pair —
// a same-namespace function (fnKey) or a statestore topic (topicKey). A
// DestinationRef holds exactly one kind, so setting both non-empty is an
// error; setting either to "" clears the destination; setting neither keeps
// current.
func destinationFromFlags(input cli.Input, fnKey, topicKey string, current *fv1.DestinationRef) (*fv1.DestinationRef, error) {
	if !input.IsSet(fnKey) && !input.IsSet(topicKey) {
		return current, nil
	}
	fnName, topic := input.String(fnKey), input.String(topicKey)
	if fnName != "" && topic != "" {
		return nil, fmt.Errorf("--%s and --%s are mutually exclusive (a destination is a function OR a topic)", fnKey, topicKey)
	}
	switch {
	case fnName != "":
		return &fv1.DestinationRef{
			Function: &fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: fnName},
		}, nil
	case topic != "":
		return &fv1.DestinationRef{
			Topic: &fv1.TopicRef{MessageQueueType: fv1.MessageQueueTypeStatestore, Topic: topic},
		}, nil
	default:
		return nil, nil // explicit empty clears the destination
	}
}

func (opts *CreateSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *CreateSubCommand) complete(input cli.Input) error {
	fnName := input.String(flagkey.FnName)

	userProvidedNS, fnNamespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error retrieving namespace information: %w", err)
	}

	// user wants a spec, create a yaml file with package and function
	toSpec := false
	if input.Bool(flagkey.SpecSave) {
		toSpec = true
		opts.specFile = fmt.Sprintf("function-%s.yaml", fnName)
	}
	specDir := util.GetSpecDir(input)
	specIgnore := util.GetSpecIgnore(input)

	if !toSpec {
		// check for unique function names within a namespace
		fn, err := opts.Client().FissionClientSet.CoreV1().Functions(fnNamespace).Get(input.Context(), input.String(flagkey.FnName), metav1.GetOptions{})
		if err != nil && !k8serrors.IsNotFound(err) {
			return err
		} else if fn.Name != "" && fn.Namespace != "" {
			return errors.New("a function with the same name already exists")
		}
	}

	entrypoint := input.String(flagkey.FnEntrypoint)

	fnTimeout := input.Int(flagkey.FnExecutionTimeout)
	if fnTimeout <= 0 {
		return fmt.Errorf("--%v must be greater than 0", flagkey.FnExecutionTimeout)
	}

	fnIdleTimeout := input.Int(flagkey.FnIdleTimeout)

	err = checkExecutorPoolManager(input, fv1.ExecutorTypePoolmgr)
	if err != nil {
		return err
	}

	fnConcurrency := DEFAULT_CONCURRENCY
	if input.IsSet(flagkey.FnConcurrency) {
		fnConcurrency = input.Int(flagkey.FnConcurrency)
	}

	requestsPerPod := input.Int(flagkey.FnRequestsPerPod)
	retainPods := input.Int(flagkey.FnRetainPods)

	fnOnceOnly := input.Bool(flagkey.FnOnceOnly)

	pkgName := input.String(flagkey.FnPackageName)

	secretNames := input.StringSlice(flagkey.FnSecret)
	cfgMapNames := input.StringSlice(flagkey.FnCfgMap)

	if input.String(flagkey.FnExecutorType) == string(fv1.ExecutorTypeContainer) {
		return fmt.Errorf("this command does not support creating function of executor type container. Check `fission function run-container --help`")
	}

	invokeStrategy, err := getInvokeStrategy(input, nil)
	if err != nil {
		return err
	}
	resourceReq, err := util.GetResourceReqs(input, &apiv1.ResourceRequirements{})
	if err != nil {
		return err
	}

	var pkgMetadata *metav1.ObjectMeta
	var envName string

	if len(pkgName) > 0 {
		var pkg *fv1.Package

		if toSpec {

			fr, err := spec.ReadSpecs(specDir, specIgnore, false)
			if err != nil {
				return fmt.Errorf("error reading spec in '%s': %w", specDir, err)
			}

			obj := fr.SpecExists(&fv1.Package{ // In case of spec I might or might not have the `fnNamespace`, how will I get pkg objectMeta here.
				ObjectMeta: metav1.ObjectMeta{
					Name:      pkgName,
					Namespace: userProvidedNS,
				},
			}, true, false)
			if obj == nil {
				return fmt.Errorf("please create package %s spec file with namespace %s before referencing it", pkgName, userProvidedNS)
			}

			pkg = obj.(*fv1.Package)
			pkgMetadata = &pkg.ObjectMeta
		} else {
			// use existing package
			pkg, err = opts.Client().FissionClientSet.CoreV1().Packages(fnNamespace).Get(input.Context(), pkgName, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("read package in '%s' in Namespace: %s. Package needs to be present in the same namespace as function: %w", pkgName, fnNamespace, err)
			}
			pkgMetadata = &pkg.ObjectMeta
		}

		envName = pkg.Spec.Environment.Name
		if envName != input.String(flagkey.FnEnvironmentName) {
			console.Warn("Function's environment is different than package's environment, package's environment will be used for creating function")
		}
	} else {
		// need to specify environment for creating new package
		envName = input.String(flagkey.FnEnvironmentName)
		if len(envName) == 0 {
			return errors.New("need --env argument")
		}

		if toSpec {

			fr, err := spec.ReadSpecs(specDir, specIgnore, false)
			if err != nil {
				return fmt.Errorf("error reading spec in '%s': %w", specDir, err)
			}
			exists, err := fr.ExistsInSpecs(fv1.Environment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      envName,
					Namespace: userProvidedNS,
				},
			})
			if err != nil {
				return err
			}
			if !exists {
				console.Warn(fmt.Sprintf("Function '%s' references unknown Environment '%s', please create it before applying spec",
					fnName, envName))
			}
		} else {
			_, err := opts.Client().FissionClientSet.CoreV1().Environments(fnNamespace).Get(input.Context(), envName, metav1.GetOptions{})
			if err != nil {
				if e, ok := err.(ferror.Error); ok && e.Code == ferror.ErrorNotFound {
					console.Warn(fmt.Sprintf("Environment \"%s\" does not exist. Please create the environment before executing the function. \nFor example: `fission env create --name %s --namespace %s --image <image>`\n", envName, envName, fnNamespace))
				} else {
					return fmt.Errorf("error retrieving environment information: %w", err)
				}
			}
		}

		srcArchiveFiles := input.StringSlice(flagkey.PkgSrcArchive)
		ociImage := input.String(flagkey.PkgOCI)
		var deployArchiveFiles []string
		noZip := false
		code := input.String(flagkey.PkgCode)
		if len(code) == 0 {
			deployArchiveFiles = input.StringSlice(flagkey.PkgDeployArchive)
		} else {
			deployArchiveFiles = append(deployArchiveFiles, input.String(flagkey.PkgCode))
			noZip = true
		}
		if err := _package.ValidateArchiveSources(code, srcArchiveFiles, deployArchiveFiles, ociImage); err != nil {
			return err
		}

		buildcmd := input.String(flagkey.PkgBuildCmd)
		pkgName := generatePackageName(fnName, uuid.NewString())

		// create new package in the same namespace as the function.
		pkgMetadata, err = _package.CreatePackage(input, opts.Client(), pkgName, fnNamespace, envName,
			srcArchiveFiles, deployArchiveFiles, buildcmd, specDir, opts.specFile, noZip, userProvidedNS, ociImage)
		if err != nil {
			return fmt.Errorf("error creating package: %w", err)
		}
	}

	// Secret/configmap references point at the function namespace when creating
	// against the cluster (and are existence-checked there), or at the
	// user-provided namespace when writing a spec (no cluster check).
	refNamespace := fnNamespace
	if toSpec {
		refNamespace = userProvidedNS
	}
	secrets, err := util.ResolveSecretReferences(input.Context(), opts.Client().KubernetesClient, secretNames, refNamespace, !toSpec, true)
	if err != nil {
		return err
	}
	cfgmaps, err := util.ResolveConfigMapReferences(input.Context(), opts.Client().KubernetesClient, cfgMapNames, refNamespace, !toSpec, true)
	if err != nil {
		return err
	}

	toolConfig, err := getToolConfig(input, nil)
	if err != nil {
		return err
	}

	invocation, err := getInvocationConfig(input, nil)
	if err != nil {
		return err
	}

	opts.function = &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fnName,
			Namespace: fnNamespace,
		},
		Spec: fv1.FunctionSpec{
			Secrets:         secrets,
			ConfigMaps:      cfgmaps,
			Resources:       *resourceReq,
			InvokeStrategy:  *invokeStrategy,
			FunctionTimeout: fnTimeout,
			IdleTimeout:     &fnIdleTimeout,
			Streaming:       getStreamingConfig(input),
			Tool:            toolConfig,
			Invocation:      invocation,
			Concurrency:     fnConcurrency,
			RequestsPerPod:  requestsPerPod,
			RetainPods:      retainPods,
			OnceOnly:        fnOnceOnly,
		},
	}

	err = util.ApplyLabelsAndAnnotations(input, &opts.function.ObjectMeta)
	if err != nil {
		return err
	}
	opts.function.Spec.Environment = fv1.EnvironmentReference{
		Name:      envName,
		Namespace: fnNamespace,
	}
	opts.function.Spec.Package = fv1.FunctionPackageRef{
		FunctionName: entrypoint,
		PackageRef: fv1.PackageRef{
			Namespace:       pkgMetadata.Namespace,
			Name:            pkgMetadata.Name,
			ResourceVersion: pkgMetadata.ResourceVersion,
		},
	}

	if toSpec {
		opts.function.Namespace = userProvidedNS
		opts.function.Spec.Package.PackageRef.Namespace = userProvidedNS
		opts.function.Spec.Environment.Namespace = userProvidedNS
	}

	return nil
}

// generatePackageName => will return package name by appending id in function name and will make sure that package name will never be more than length of 63 characters.
func generatePackageName(fnName string, id string) string {
	var (
		lenFnName       = len(fnName)
		lenId           = len(id)
		lastIndexOfChar int
	)
	if lenFnName+lenId <= 62 {
		return fmt.Sprintf("%s-%s", fnName, id)
	}

	lastIndexOfChar = lenFnName - (lenFnName + lenId - 62)
	pkgName := fmt.Sprintf("%v-%s", fnName[:lastIndexOfChar], id)
	console.Info(fmt.Sprintf("Generated package %s from function to acceptable character limit", pkgName))
	return pkgName
}

// run write the resource to a spec file or create a fission CRD with remote fission server.
// It also prints warning/error if necessary.
func (opts *CreateSubCommand) run(input cli.Input) error {
	// if we're writing a spec, don't create the function; save/print and return.
	if handled, err := spec.SaveOrDry(input, *opts.function, opts.specFile); handled {
		return err
	}

	_, err := opts.Client().FissionClientSet.CoreV1().Functions(opts.function.ObjectMeta.Namespace).Create(input.Context(), opts.function, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("error creating function: %w", err)
	}

	fmt.Printf("function '%s' created\n", opts.function.Name)

	// Allow the user to specify an HTTP trigger while creating a function.
	triggerUrl := input.String(flagkey.HtUrl)
	prefix := input.String(flagkey.HtPrefix)
	if len(triggerUrl) == 0 && len(prefix) == 0 {
		return nil
	}
	if len(prefix) != 0 && len(triggerUrl) > 0 {
		console.Warn("Prefix will take precedence over URL/RelativeURL")
	}

	methods := input.StringSlice(flagkey.HtMethod)
	if len(methods) == 0 {
		return errors.New("HTTP methods not mentioned")
	}

	for _, method := range methods {
		_, err := httptrigger.GetMethod(method)
		if err != nil {
			return err
		}
	}

	triggerName := uuid.NewString()
	ht := &fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      triggerName,
			Namespace: opts.function.Namespace,
		},
		Spec: fv1.HTTPTriggerSpec{
			RelativeURL: triggerUrl,
			Prefix:      &prefix,
			Methods:     methods,
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: opts.function.Name,
			},
		},
	}
	_, err = opts.Client().FissionClientSet.CoreV1().HTTPTriggers(opts.function.ObjectMeta.Namespace).Create(input.Context(), ht, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("error creating HTTP trigger: %w", err)
	}

	fmt.Printf("route created: %s %s -> %s\n", methods, triggerUrl, opts.function.Name)
	return nil
}

func getInvokeStrategy(input cli.Input, existingInvokeStrategy *fv1.InvokeStrategy) (strategy *fv1.InvokeStrategy, err error) {
	var es *fv1.ExecutionStrategy

	if existingInvokeStrategy == nil {
		executorType, err := getExecutorType(input)
		if err != nil {
			return nil, err
		}
		es, err = getExecutionStrategy(executorType, input)
		if err != nil {
			return nil, err
		}
	} else {
		es, err = updateExecutionStrategy(input, &existingInvokeStrategy.ExecutionStrategy)
		if err != nil {
			return nil, err
		}
	}

	return &fv1.InvokeStrategy{
		ExecutionStrategy: *es,
		StrategyType:      fv1.StrategyTypeExecution,
	}, nil
}

// Show warning when --con, --rpp and --yolo flags are used with executortype other than `poolmgr`.
// These flags are specifically introduced for executortype `poolmgr`.
func checkExecutorPoolManager(input cli.Input, existingExecutorType fv1.ExecutorType) error {
	var isNotPoolManager bool
	if input.IsSet(flagkey.EnvExecutorType) {
		executorType, err := getExecutorType(input)
		if err != nil {
			return err
		}
		isNotPoolManager = (string(executorType) != string(fv1.ExecutorTypePoolmgr))
	} else {
		isNotPoolManager = (string(existingExecutorType) != string(fv1.ExecutorTypePoolmgr))
	}

	if input.IsSet(flagkey.FnConcurrency) && isNotPoolManager {
		console.Warn("--concurrency is only valid for executortype; `poolmgr`. Check `fission function create --help`")
	}
	if input.IsSet(flagkey.FnRequestsPerPod) && isNotPoolManager {
		console.Warn("--requestsperpod is only valid for executortype; `poolmgr`. Check `fission function create --help`")
	}
	if input.IsSet(flagkey.FnOnceOnly) && isNotPoolManager {
		console.Warn("--onceonly is only valid for executortype; `poolmgr`. Check `fission function create --help`")
	}

	return nil
}

func getExecutorType(input cli.Input) (executorType fv1.ExecutorType, err error) {
	switch input.String(flagkey.FnExecutorType) {
	case "":
		fallthrough
	case string(fv1.ExecutorTypePoolmgr):
		executorType = fv1.ExecutorTypePoolmgr
	case string(fv1.ExecutorTypeNewdeploy):
		executorType = fv1.ExecutorTypeNewdeploy
	case string(fv1.ExecutorTypeContainer):
		executorType = fv1.ExecutorTypeContainer
	default:
		err = fmt.Errorf("executor type must be one of '%v', '%v' or '%v'", fv1.ExecutorTypePoolmgr, fv1.ExecutorTypeNewdeploy, fv1.ExecutorTypeContainer)
	}
	return executorType, err
}

func getExecutionStrategy(fnExecutor fv1.ExecutorType, input cli.Input) (strategy *fv1.ExecutionStrategy, err error) {
	specializationTimeout := fv1.DefaultSpecializationTimeOut

	if input.IsSet(flagkey.FnSpecializationTimeout) {
		specializationTimeout = input.Int(flagkey.FnSpecializationTimeout)
		if specializationTimeout < fv1.DefaultSpecializationTimeOut {
			return nil, fmt.Errorf("%v must be greater than or equal to 120 seconds", flagkey.FnSpecializationTimeout)
		}
	}

	if fnExecutor == fv1.ExecutorTypePoolmgr {
		if input.IsSet(flagkey.RuntimeTargetcpu) || input.IsSet(flagkey.ReplicasMinscale) || input.IsSet(flagkey.ReplicasMaxscale) {
			return nil, errors.New("to set target CPU or min/max scale for function, please specify \"--executortype newdeploy\"")
		}

		if input.IsSet(flagkey.RuntimeMincpu) || input.IsSet(flagkey.RuntimeMaxcpu) || input.IsSet(flagkey.RuntimeMinmemory) || input.IsSet(flagkey.RuntimeMaxmemory) {
			console.Warn("To limit CPU/Memory for function with executor type \"poolmgr\", please specify resources limits when creating environment")
		}

		strategy = &fv1.ExecutionStrategy{
			ExecutorType:          fv1.ExecutorTypePoolmgr,
			SpecializationTimeout: specializationTimeout,
		}
	} else {

		minScale := DEFAULT_MIN_SCALE
		if input.IsSet(flagkey.ReplicasMinscale) {
			minScale = input.Int(flagkey.ReplicasMinscale)
		}

		maxScale := minScale
		if input.IsSet(flagkey.ReplicasMaxscale) {
			maxScale = input.Int(flagkey.ReplicasMaxscale)
			if maxScale <= 0 {
				return nil, fmt.Errorf("%v must be greater than 0", flagkey.ReplicasMaxscale)
			}
		}

		if minScale > maxScale {
			return nil, fmt.Errorf("minscale (%v) can not be greater than maxscale (%v)", minScale, maxScale)
		}

		// Right now a simple single case strategy implementation
		// This will potentially get more sophisticated once we have more strategies in place
		strategy = &fv1.ExecutionStrategy{
			ExecutorType:          fnExecutor,
			MinScale:              minScale,
			MaxScale:              maxScale,
			SpecializationTimeout: specializationTimeout,
		}

		if input.IsSet(flagkey.RuntimeTargetcpu) {
			targetCPU, err := getTargetCPU(input)
			if err != nil {
				return nil, err
			}
			strategy.Metrics = []asv2.MetricSpec{hpa.ConvertTargetCPUToCustomMetric(int32(targetCPU))}
		}
	}

	return strategy, nil
}

func updateExecutionStrategy(input cli.Input, existingExecutionStrategy *fv1.ExecutionStrategy) (strategy *fv1.ExecutionStrategy, err error) {
	fnExecutor := existingExecutionStrategy.ExecutorType
	oldExecutor := existingExecutionStrategy.ExecutorType

	if input.IsSet(flagkey.FnExecutorType) {
		switch input.String(flagkey.FnExecutorType) {
		case "":
			fallthrough
		case string(fv1.ExecutorTypePoolmgr):
			fnExecutor = fv1.ExecutorTypePoolmgr
		case string(fv1.ExecutorTypeNewdeploy):
			fnExecutor = fv1.ExecutorTypeNewdeploy
		case string(fv1.ExecutorTypeContainer):
			fnExecutor = fv1.ExecutorTypeContainer
		default:
			return nil, fmt.Errorf("executor type must be one of '%v', %v or '%v'", fv1.ExecutorTypePoolmgr, fv1.ExecutorTypeNewdeploy, fv1.ExecutorTypeContainer)
		}
	}

	specializationTimeout := existingExecutionStrategy.SpecializationTimeout

	if input.IsSet(flagkey.FnSpecializationTimeout) {
		specializationTimeout = input.Int(flagkey.FnSpecializationTimeout)
		if specializationTimeout < fv1.DefaultSpecializationTimeOut {
			return nil, fmt.Errorf("%v must be greater than or equal to 120 seconds", flagkey.FnSpecializationTimeout)
		}
	} else {
		if specializationTimeout < fv1.DefaultSpecializationTimeOut {
			specializationTimeout = fv1.DefaultSpecializationTimeOut
		}
	}

	if fnExecutor == fv1.ExecutorTypePoolmgr {
		if input.IsSet(flagkey.RuntimeTargetcpu) || input.IsSet(flagkey.ReplicasMinscale) || input.IsSet(flagkey.ReplicasMaxscale) {
			return nil, errors.New("to set target CPU or min/max scale for function, please specify \"--executortype newdeploy\"")
		}

		if input.IsSet(flagkey.RuntimeMincpu) || input.IsSet(flagkey.RuntimeMaxcpu) || input.IsSet(flagkey.RuntimeMinmemory) || input.IsSet(flagkey.RuntimeMaxmemory) {
			console.Warn("To limit CPU/Memory for function with executor type \"poolmgr\", please specify resources limits when creating environment")
		}
		strategy = &fv1.ExecutionStrategy{
			ExecutorType:          fv1.ExecutorTypePoolmgr,
			SpecializationTimeout: specializationTimeout,
		}
	} else {
		minScale := existingExecutionStrategy.MinScale
		maxScale := existingExecutionStrategy.MaxScale

		if fnExecutor != oldExecutor { // from poolmanager to newdeploy
			minScale = DEFAULT_MIN_SCALE
			maxScale = minScale
		}

		if input.IsSet(flagkey.ReplicasMinscale) {
			minScale = input.Int(flagkey.ReplicasMinscale)
		}

		if input.IsSet(flagkey.ReplicasMaxscale) {
			maxScale = input.Int(flagkey.ReplicasMaxscale)
			if maxScale <= 0 {
				return nil, fmt.Errorf("%v must be greater than 0", flagkey.ReplicasMaxscale)
			}
		} else {
			if maxScale <= 0 {
				maxScale = 1
			}
		}

		if minScale > maxScale {
			return nil, fmt.Errorf("minscale (%v) can not be greater than maxscale (%v)", minScale, maxScale)
		}

		// Right now a simple single case strategy implementation
		// This will potentially get more sophisticated once we have more strategies in place
		strategy = &fv1.ExecutionStrategy{
			ExecutorType:          fnExecutor,
			MinScale:              minScale,
			MaxScale:              maxScale,
			SpecializationTimeout: specializationTimeout,
		}

		if input.IsSet(flagkey.RuntimeTargetcpu) {
			targetCPU, err := getTargetCPU(input)
			if err != nil {
				return nil, err
			}
			strategy.Metrics = []asv2.MetricSpec{hpa.ConvertTargetCPUToCustomMetric(int32(targetCPU))}
		}

	}

	return strategy, nil
}

func getTargetCPU(input cli.Input) (int, error) {
	targetCPU := input.Int(flagkey.RuntimeTargetcpu)
	if targetCPU <= 0 || targetCPU > 100 {
		return 0, fmt.Errorf("%v must be a value between 1 - 100", flagkey.RuntimeTargetcpu)
	}
	return targetCPU, nil
}
