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

package function

import (
	"fmt"

	"github.com/pkg/errors"
	uuid "github.com/satori/go.uuid"
	asv2 "k8s.io/api/autoscaling/v2"
	apiv1 "k8s.io/api/core/v1"
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

func (opts *CreateSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *CreateSubCommand) complete(input cli.Input) error {
	fnName := input.String(flagkey.FnName)

	userProvidedNS, fnNamespace, err := opts.GetResourceNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return errors.Wrap(err, "error retrieving namespace information")
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
		return errors.Errorf("--%v must be greater than 0", flagkey.FnExecutionTimeout)
	}

	fnIdleTimeout := input.Int(flagkey.FnIdleTimeout)

	fnConcurrency := DEFAULT_CONCURRENCY
	if input.IsSet(flagkey.FnConcurrency) {
		fnConcurrency = input.Int(flagkey.FnConcurrency)
	}

	requestsPerPod := input.Int(flagkey.FnRequestsPerPod)

	fnOnceOnly := input.Bool(flagkey.FnOnceOnly)

	pkgName := input.String(flagkey.FnPackageName)

	secretNames := input.StringSlice(flagkey.FnSecret)
	cfgMapNames := input.StringSlice(flagkey.FnCfgMap)

	if input.String(flagkey.FnExecutorType) == string(fv1.ExecutorTypeContainer) {
		return errors.Errorf("this command does not support creating function of executor type container. Check `fission function run-container --help`")
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
				return errors.Wrap(err, fmt.Sprintf("error reading spec in '%s'", specDir))
			}

			obj := fr.SpecExists(&fv1.Package{ // In case of spec I might or might not have the `fnNamespace`, how will I get pkg objectMeta here.
				ObjectMeta: metav1.ObjectMeta{
					Name:      pkgName,
					Namespace: userProvidedNS,
				},
			}, true, false)
			if obj == nil {
				return errors.Errorf("please create package %s spec file with namespace %s before referencing it", pkgName, userProvidedNS)
			}

			pkg = obj.(*fv1.Package)
			pkgMetadata = &pkg.ObjectMeta
		} else {
			// use existing package
			pkg, err = opts.Client().FissionClientSet.CoreV1().Packages(fnNamespace).Get(input.Context(), pkgName, metav1.GetOptions{})
			if err != nil {
				return errors.Wrap(err, fmt.Sprintf("read package in '%s' in Namespace: %s. Package needs to be present in the same namespace as function", pkgName, fnNamespace))
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
				return errors.Wrap(err, fmt.Sprintf("error reading spec in '%s'", specDir))
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
					console.Warn(fmt.Sprintf("Environment \"%s\" does not exist. Please create the environment before executing the function. \nFor example: `fission env create --name %s --envns %s --image <image>`\n", envName, envName, fnNamespace))
				} else {
					return errors.Wrap(err, "error retrieving environment information")
				}
			}
		}

		srcArchiveFiles := input.StringSlice(flagkey.PkgSrcArchive)
		var deployArchiveFiles []string
		noZip := false
		code := input.String(flagkey.PkgCode)
		if len(code) == 0 {
			deployArchiveFiles = input.StringSlice(flagkey.PkgDeployArchive)
		} else {
			deployArchiveFiles = append(deployArchiveFiles, input.String(flagkey.PkgCode))
			noZip = true
		}
		// return error when both src & deploy archive are empty
		if len(srcArchiveFiles) == 0 && len(deployArchiveFiles) == 0 {
			return errors.New("need --code or --deploy or --src argument")
		}

		buildcmd := input.String(flagkey.PkgBuildCmd)
		id, err := uuid.NewV4()
		if err != nil {
			return errors.Wrap(err, "error generating uuid")
		}
		pkgName := generatePackageName(fnName, id.String())

		// create new package in the same namespace as the function.
		pkgMetadata, err = _package.CreatePackage(input, opts.Client(), pkgName, fnNamespace, envName,
			srcArchiveFiles, deployArchiveFiles, buildcmd, specDir, opts.specFile, noZip, userProvidedNS)
		if err != nil {
			return errors.Wrap(err, "error creating package")
		}
	}

	var secrets []fv1.SecretReference
	var cfgmaps []fv1.ConfigMapReference

	if len(secretNames) > 0 {
		// check the referenced secret is in the same ns as the function, if not give a warning.
		if !toSpec { // TODO: workaround in order not to block users from creating function spec, remove it.
			for _, secretName := range secretNames {
				err := util.SecretExists(input.Context(), &metav1.ObjectMeta{Namespace: fnNamespace, Name: secretName}, opts.Client().KubernetesClient)
				if err != nil {
					if k8serrors.IsNotFound(err) {
						console.Warn(fmt.Sprintf("Secret %s not found in Namespace: %s. Secret needs to be present in the same namespace as function", secretName, fnNamespace))
					} else {
						return errors.Wrapf(err, "error checking secret %s", secretName)
					}
				}
				newSecret := fv1.SecretReference{
					Name:      secretName,
					Namespace: fnNamespace,
				}
				secrets = append(secrets, newSecret)
			}
		} else {
			for _, secretName := range secretNames {
				newSecret := fv1.SecretReference{
					Name:      secretName,
					Namespace: userProvidedNS,
				}
				secrets = append(secrets, newSecret)
			}
		}

	}

	if len(cfgMapNames) > 0 {
		// check the referenced cfgmap is in the same ns as the function, if not give a warning.
		if !toSpec {
			for _, cfgMapName := range cfgMapNames {
				err := util.ConfigMapExists(input.Context(), &metav1.ObjectMeta{Namespace: fnNamespace, Name: cfgMapName}, opts.Client().KubernetesClient)
				if err != nil {
					if k8serrors.IsNotFound(err) {
						console.Warn(fmt.Sprintf("ConfigMap %s not found in Namespace: %s. ConfigMap needs to be present in the same namespace as function", cfgMapName, fnNamespace))
					} else {
						return errors.Wrapf(err, "error checking configmap %s", cfgMapName)
					}
				}
				newCfgMap := fv1.ConfigMapReference{
					Name:      cfgMapName,
					Namespace: fnNamespace,
				}
				cfgmaps = append(cfgmaps, newCfgMap)
			}
		} else {
			for _, cfgMapName := range cfgMapNames {
				newCfgMap := fv1.ConfigMapReference{
					Name:      cfgMapName,
					Namespace: userProvidedNS,
				}
				cfgmaps = append(cfgmaps, newCfgMap)
			}
		}
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
			Concurrency:     fnConcurrency,
			RequestsPerPod:  requestsPerPod,
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
		opts.function.ObjectMeta.Namespace = userProvidedNS
		opts.function.Spec.Package.PackageRef.Namespace = userProvidedNS
		opts.function.Spec.Environment.Namespace = userProvidedNS
	}

	return nil
}

// generatePackgeName => will return package name by appending id in function name and will make sure that package name will never be more than length of 63 characters.
func generatePackageName(fnName string, id string) string {
	var (
		lenFnName       int = len(fnName)
		lenId           int = len(id)
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
	// if we're writing a spec, don't create the function
	// save to spec file or display the spec to console
	if input.Bool(flagkey.SpecDry) {
		return spec.SpecDry(*opts.function)
	}

	if input.Bool(flagkey.SpecSave) {
		err := spec.SpecSave(*opts.function, opts.specFile)
		if err != nil {
			return errors.Wrap(err, "error saving function spec")
		}
		return nil
	}

	_, err := opts.Client().FissionClientSet.CoreV1().Functions(opts.function.ObjectMeta.Namespace).Create(input.Context(), opts.function, metav1.CreateOptions{})
	if err != nil {
		return errors.Wrap(err, "error creating function")
	}

	fmt.Printf("function '%s' created\n", opts.function.ObjectMeta.Name)

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

	id, err := uuid.NewV4()
	if err != nil {
		return errors.Wrap(err, "error generating UUID")
	}
	triggerName := id.String()
	ht := &fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      triggerName,
			Namespace: opts.function.ObjectMeta.Namespace,
		},
		Spec: fv1.HTTPTriggerSpec{
			RelativeURL: triggerUrl,
			Prefix:      &prefix,
			Methods:     methods,
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: opts.function.ObjectMeta.Name,
			},
		},
	}
	_, err = opts.Client().FissionClientSet.CoreV1().HTTPTriggers(opts.function.ObjectMeta.Namespace).Create(input.Context(), ht, metav1.CreateOptions{})
	if err != nil {
		return errors.Wrap(err, "error creating HTTP trigger")
	}

	fmt.Printf("route created: %s %s -> %s\n", methods, triggerUrl, opts.function.ObjectMeta.Name)
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
		err = errors.Errorf("executor type must be one of '%v', '%v' or '%v'", fv1.ExecutorTypePoolmgr, fv1.ExecutorTypeNewdeploy, fv1.ExecutorTypeContainer)
	}
	return executorType, err
}

func getExecutionStrategy(fnExecutor fv1.ExecutorType, input cli.Input) (strategy *fv1.ExecutionStrategy, err error) {
	specializationTimeout := fv1.DefaultSpecializationTimeOut

	if input.IsSet(flagkey.FnSpecializationTimeout) {
		specializationTimeout = input.Int(flagkey.FnSpecializationTimeout)
		if specializationTimeout < fv1.DefaultSpecializationTimeOut {
			return nil, errors.Errorf("%v must be greater than or equal to 120 seconds", flagkey.FnSpecializationTimeout)
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
				return nil, errors.Errorf("%v must be greater than 0", flagkey.ReplicasMaxscale)
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
			return nil, errors.Errorf("executor type must be one of '%v', %v or '%v'", fv1.ExecutorTypePoolmgr, fv1.ExecutorTypeNewdeploy, fv1.ExecutorTypeContainer)
		}
	}

	specializationTimeout := existingExecutionStrategy.SpecializationTimeout

	if input.IsSet(flagkey.FnSpecializationTimeout) {
		specializationTimeout = input.Int(flagkey.FnSpecializationTimeout)
		if specializationTimeout < fv1.DefaultSpecializationTimeOut {
			return nil, errors.Errorf("%v must be greater than or equal to 120 seconds", flagkey.FnSpecializationTimeout)
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
				return nil, errors.Errorf("%v must be greater than 0", flagkey.ReplicasMaxscale)
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
		return 0, errors.Errorf("%v must be a value between 1 - 100", flagkey.RuntimeTargetcpu)
	}
	return targetCPU, nil
}
