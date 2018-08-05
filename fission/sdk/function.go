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

package sdk

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/satori/go.uuid"
	"k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	multierror "github.com/hashicorp/go-multierror"

	"github.com/fission/fission"
	controllerClient "github.com/fission/fission/controller/client"
	"github.com/fission/fission/crd"
	"github.com/fission/fission/fission/log"
)

const (
	//File names with common source code extensions
	SINGLE_SOURCE_CODE_FILENAME_PATTERN = `\.(js|php|go|py|rb)$`
)

type CreateFunctionArgs struct {
	FnName            string
	Spec              bool
	EntryPoint        string
	PkgName           string
	SecretName        string
	CfgMapName        string
	EnvName           string
	SrcArchivePath    string
	CodePath          string
	CodeLiteral       string
	DeployArchivePath string
	BuildCommand      string
	TriggerURL        string
	Method            string
	MinScale          int
	MaxScale          int
	ExecutorType      string
	MinCPU            int
	MaxCPU            int
	MinMemory         int
	MaxMemory         int
	TargetCPU         int
	FnNamespace       string
	EnvNamespace      string
	Client            *controllerClient.Client
}

func (arg CreateFunctionArgs) validate() error {

	var result *multierror.Error

	if len(arg.FnName) == 0 {
		result = multierror.Append(result, MissingArgError("name"))
	}

	if len(arg.EnvName) == 0 && len(arg.PkgName) == 0 {
		result = multierror.Append(result, MissingArgError("env"))
	}

	numCodeArgs := 0
	if len(arg.CodePath) > 0 {
		numCodeArgs++
	}
	if len(arg.SrcArchivePath) > 0 {
		numCodeArgs++
	}
	if len(arg.DeployArchivePath) > 0 {
		numCodeArgs++
	}
	if len(arg.CodeLiteral) > 0 {
		numCodeArgs++
	}
	if numCodeArgs != 1 {
		if log.IsCliRun {
			result = multierror.Append(result, GeneralError("Missing argument. Need exactly one of --code, --deployarchive or --sourcearchive"))
		} else {
			result = multierror.Append(result, GeneralError("Missing argument. Need exactly one of CodePath, CodeLiteral, DeployArchivePath or SrcArchivePath"))
		}

	}
	if isIndividualSourceCodeFile(arg.SrcArchivePath) {
		result = multierror.Append(result, GeneralError(fmt.Sprintf(
			"Invalid argument: --sourcearchive '%v' refers to an individual source code file not an archive. "+
				"For single source file use --code instead. Regexp used for validation: `%v`",
			arg.SrcArchivePath, SINGLE_SOURCE_CODE_FILENAME_PATTERN)))
	}
	if isIndividualSourceCodeFile(arg.DeployArchivePath) {
		result = multierror.Append(result, GeneralError(fmt.Sprintf(
			"Invalid argument: --deployarchive '%v' refers to an individual source code file not an archive. "+
				"For single source file use --code instead. Regexp used for validation: `%v`",
			arg.DeployArchivePath, SINGLE_SOURCE_CODE_FILENAME_PATTERN)))
	}

	// check for unique function names within a namespace
	fnList, err := arg.Client.FunctionList(arg.FnNamespace)
	if err != nil {
		result = multierror.Append(result, FailedToError(err, "get function list"))
	}

	// check function existence before creating package
	// From this change onwards, we mandate that a function should reference a secret, config map and package
	for _, fn := range fnList {
		if fn.Metadata.Name == arg.FnName {
			result = multierror.Append(result, GeneralError("A function with the same name already exists."))
		}
	}

	// examine existence of given environment
	if len(arg.EnvName) != 0 {
		_, err = arg.Client.EnvironmentGet(&metav1.ObjectMeta{
			Namespace: metav1.NamespaceDefault,
			Name:      arg.EnvName,
		})

		if err != nil {
			if e, ok := err.(fission.Error); ok && e.Code == fission.ErrorNotFound {
				result = multierror.Append(result, GeneralError(fmt.Sprintf("Environment \"%v\" does not exist. Please create the environment before executing the function. \nFor example: `fission env create --name %v --image <image>`\n", arg.EnvName, arg.EnvName)))
			} else {
				result = multierror.Append(result, FailedToError(err, "retrieve environment information"))
			}
		}
	}
	if arg.Client == nil {
		result = multierror.Append(result, GeneralError("Client must be specified on CreateFunctionArgs"))
	}

	return result.ErrorOrNil()

}

func getInvokeStrategy(minScale int, maxScale int, executorType string, targetcpu int) (fission.InvokeStrategy, error) {

	if maxScale == 0 {
		maxScale = 1
	}

	if minScale > maxScale {
		return fission.InvokeStrategy{}, GeneralError("Maxscale must be higher than or equal to minscale")
	}

	var fnExecutor fission.ExecutorType
	switch executorType {
	case "":
		fnExecutor = fission.ExecutorTypePoolmgr
	case fission.ExecutorTypePoolmgr:
		fnExecutor = fission.ExecutorTypePoolmgr
	case fission.ExecutorTypeNewdeploy:
		fnExecutor = fission.ExecutorTypeNewdeploy
	default:
		return fission.InvokeStrategy{}, GeneralError("Executor type must be one of 'poolmgr' or 'newdeploy', defaults to 'poolmgr'")
	}

	// Right now a simple single case strategy implementation
	// This will potentially get more sophisticated once we have more strategies in place
	strategy := fission.InvokeStrategy{
		StrategyType: fission.StrategyTypeExecution,
		ExecutionStrategy: fission.ExecutionStrategy{
			ExecutorType:     fnExecutor,
			MinScale:         minScale,
			MaxScale:         maxScale,
			TargetCPUPercent: targetcpu,
		},
	}
	return strategy, nil
}

func CreateFunction(args *CreateFunctionArgs) error {

	//If not called from CLI these could be empty so default here too
	if len(args.FnNamespace) == 0 {
		args.FnNamespace = metav1.NamespaceDefault
	}
	if len(args.EnvNamespace) == 0 {
		args.EnvNamespace = metav1.NamespaceDefault
	}

	log.Verbosef(2, "CreateFunction with args %+v\n", args)

	err := args.validate()
	if err != nil {
		return err
	}

	fnName := args.FnName
	spec := args.Spec
	entrypoint := args.EntryPoint
	pkgName := args.PkgName
	secretName := args.SecretName
	cfgMapName := args.CfgMapName
	envName := args.EnvName
	srcArchivePath := args.SrcArchivePath
	deployArchivePath := args.DeployArchivePath
	buildCommand := args.BuildCommand
	triggerURL := args.TriggerURL
	method := args.Method
	minscale := args.MinScale
	maxscale := args.MaxScale
	executortype := args.ExecutorType
	targetCPU := args.TargetCPU
	client := args.Client
	mincpu := args.MinCPU
	maxcpu := args.MaxCPU
	minmemory := args.MinMemory
	maxmemory := args.MaxMemory
	fnNamespace := args.FnNamespace
	envNamespace := args.EnvNamespace

	//For user clarity we only allow one of --code/--deployarchive/--sourcearchive to be specified - see validate()
	//But internally a single source code file is still treated as a deployArchive
	if len(args.CodePath) > 0 {
		deployArchivePath = args.CodePath
	}
	//For SDK also allow code as a literal string
	if len(args.CodeLiteral) > 0 {
		codeTempPath, err := SaveStringToTempFile(args.CodeLiteral)
		if err != nil {
			return FailedToError(err, "save code literal to temp file")
		}
		deployArchivePath = codeTempPath
	}

	resourceReq := GetResourceReq(mincpu, maxcpu, minmemory, maxmemory, v1.ResourceRequirements{})
	targetCPU, err = GetTargetCPU(targetCPU)
	if err != nil {
		return err
	}

	// user wants a spec, create a yaml file with package and function
	specFile := ""
	if spec {
		specFile = fmt.Sprintf("function-%v.yaml", fnName)
	}

	var pkgMetadata *metav1.ObjectMeta
	if len(pkgName) > 0 {
		// use existing package
		pkg, err := client.PackageGet(&metav1.ObjectMeta{
			Namespace: fnNamespace,
			Name:      pkgName,
		})
		if err != nil {
			return FailedToError(err, fmt.Sprintf("read package in '%v' in Namespace: %s. Package needs to be present in the same namespace as function", pkgName, fnNamespace))
		}

		pkgMetadata = &pkg.Metadata
		envName = pkg.Spec.Environment.Name
		envNamespace = pkg.Spec.Environment.Namespace
	} else {

		// create new package in the same namespace as the function.
		pkgMetadata, err = CreatePackage(client, fnNamespace, envName, envNamespace, srcArchivePath, deployArchivePath, buildCommand, specFile)
		if err != nil {
			return FailedToError(err, "create new package")
		}
	}

	invokeStrategy, err := getInvokeStrategy(minscale, maxscale, executortype, targetCPU)
	if err != nil {
		return FailedToError(err, "get invoke strategy")
	}
	if (mincpu != 0 || maxcpu != 0 || minmemory != 0 || maxmemory != 0) &&
		invokeStrategy.ExecutionStrategy.ExecutorType == fission.ExecutorTypePoolmgr {
		log.Warn("CPU/Memory specified for function with pool manager executor will be ignored in favor of resources specified at environment")
	}

	var secrets []fission.SecretReference
	var cfgmaps []fission.ConfigMapReference

	if len(secretName) > 0 {
		// check the referenced secret is in the same ns as the function, if not give a warning.
		_, err := client.SecretGet(&metav1.ObjectMeta{
			Namespace: fnNamespace,
			Name:      secretName,
		})
		if k8serrors.IsNotFound(err) {
			log.Warn(fmt.Sprintf("Secret %s not found in Namespace: %s. Secret needs to be present in the same namespace as function", secretName, fnNamespace))
		}

		newSecret := fission.SecretReference{
			Name:      secretName,
			Namespace: fnNamespace,
		}
		secrets = []fission.SecretReference{newSecret}
	}

	if len(cfgMapName) > 0 {
		// check the referenced cfgmap is in the same ns as the function, if not give a warning.
		_, err := client.ConfigMapGet(&metav1.ObjectMeta{
			Namespace: fnNamespace,
			Name:      cfgMapName,
		})
		if k8serrors.IsNotFound(err) {
			log.Warn(fmt.Sprintf("ConfigMap %s not found in Namespace: %s. ConfigMap needs to be present in the same namespace as function", cfgMapName, fnNamespace))
		}

		newCfgMap := fission.ConfigMapReference{
			Name:      cfgMapName,
			Namespace: fnNamespace,
		}
		cfgmaps = []fission.ConfigMapReference{newCfgMap}
	}

	function := &crd.Function{
		Metadata: metav1.ObjectMeta{
			Name:      fnName,
			Namespace: fnNamespace,
		},
		Spec: fission.FunctionSpec{
			Environment: fission.EnvironmentReference{
				Name:      envName,
				Namespace: envNamespace,
			},
			Package: fission.FunctionPackageRef{
				FunctionName: entrypoint,
				PackageRef: fission.PackageRef{
					Namespace:       pkgMetadata.Namespace,
					Name:            pkgMetadata.Name,
					ResourceVersion: pkgMetadata.ResourceVersion,
				},
			},
			Secrets:        secrets,
			ConfigMaps:     cfgmaps,
			Resources:      resourceReq,
			InvokeStrategy: invokeStrategy,
		},
	}

	// if we're writing a spec, don't create the function
	if spec {
		err = SpecSave(*function, specFile)
		if err != nil {
			return FailedToError(err, "create function spec")
		}
		return nil
	}

	_, err = client.FunctionCreate(function)
	if err != nil {
		return FailedToError(err, "create function")
	}

	fmt.Printf("function '%v' created\n", fnName)

	// Allow the user to specify an HTTP trigger while creating a function.
	if len(triggerURL) == 0 {
		return nil
	}
	if !strings.HasPrefix(triggerURL, "/") {
		triggerURL = fmt.Sprintf("/%s", triggerURL)
	}

	if len(method) == 0 {
		method = "GET"
	}
	triggerName := uuid.NewV4().String()
	ht := &crd.HTTPTrigger{
		Metadata: metav1.ObjectMeta{
			Name:      triggerName,
			Namespace: fnNamespace,
		},
		Spec: fission.HTTPTriggerSpec{
			RelativeURL: triggerURL,
			Method:      GetMethod(method),
			FunctionReference: fission.FunctionReference{
				Type: fission.FunctionReferenceTypeFunctionName,
				Name: fnName,
			},
		},
	}
	_, err = client.HTTPTriggerCreate(ht)
	if err != nil {
		return FailedToError(err, "create HTTP trigger")
	}
	fmt.Printf("route created: %v %v -> %v\n", method, triggerURL, fnName)

	return err
}

func GetTargetCPU(targetCPU int) (int, error) {
	if targetCPU != 0 {
		if targetCPU <= 0 || targetCPU > 100 {
			return 0, GeneralError("TargetCPU must be a value between 1 - 100")
		}
	} else {
		targetCPU = 80
	}
	return targetCPU, nil
}

func isIndividualSourceCodeFile(filename string) bool {
	if len(filename) == 0 {
		return false
	}
	//Ideally we'd probably check that this is an archive not plain text, but this is a start
	matched, err := regexp.MatchString(SINGLE_SOURCE_CODE_FILENAME_PATTERN, filename)
	if err != nil {
		return false
	}
	return matched
}
