/*
Copyright 2018 The Fission Authors.

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

package util

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/controller/client/rest"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/info"
	"github.com/fission/fission/pkg/plugin"
	"github.com/fission/fission/pkg/utils"
)

func GetFissionNamespace() string {
	fissionNamespace := os.Getenv("FISSION_NAMESPACE")
	return fissionNamespace
}

func GetApplicationUrl(selector string, kubeContext string) (string, error) {
	var serverUrl string
	// Use FISSION_URL env variable if set; otherwise, port-forward to controller.
	fissionUrl := os.Getenv("FISSION_URL")
	if len(fissionUrl) == 0 {
		fissionNamespace := GetFissionNamespace()
		localPort, err := SetupPortForward(fissionNamespace, selector, kubeContext)
		if err != nil {
			return "", err
		}
		serverUrl = "http://127.0.0.1:" + localPort
	} else {
		serverUrl = fissionUrl
	}
	return serverUrl, nil
}

// KubifyName make a kubernetes compliant name out of an arbitrary string
func KubifyName(old string) string {
	// Kubernetes maximum name length (for some names; others can be 253 chars)
	maxLen := 63

	newName := strings.ToLower(old)

	// replace disallowed chars with '-'
	inv, _ := regexp.Compile("[^-a-z0-9]")
	newName = string(inv.ReplaceAll([]byte(newName), []byte("-")))

	// trim leading non-alphabetic
	leadingnonalpha, _ := regexp.Compile("^[^a-z]+")
	newName = string(leadingnonalpha.ReplaceAll([]byte(newName), []byte{}))

	// trim trailing
	trailing, _ := regexp.Compile("[^a-z0-9]+$")
	newName = string(trailing.ReplaceAll([]byte(newName), []byte{}))

	// truncate to length
	if len(newName) > maxLen {
		newName = newName[0:maxLen]
	}

	// if we removed everything, call this thing "default". maybe
	// we should generate a unique name...
	if len(newName) == 0 {
		newName = "default"
	}

	return newName
}

// GetKubernetesClient builds a new kubernetes client. If the KUBECONFIG
// environment variable is empty or doesn't exist, ~/.kube/config is used for
// the kube config path
func GetKubernetesClient(kubeContext string) (*restclient.Config, *kubernetes.Clientset, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()

	kubeConfigPath := os.Getenv("KUBECONFIG")
	if len(kubeConfigPath) == 0 {
		var homeDir string
		usr, err := user.Current()
		if err != nil {
			// In case that user.Current() may be unable to work under some circumstances and return errors like
			// "user: Current not implemented on darwin/amd64" due to cross-compilation problem. (https://github.com/golang/go/issues/6376).
			// Instead of doing fatal here, we fallback to get home directory from the environment $HOME.
			console.Warn(fmt.Sprintf("Could not get the current user's directory (%s), fallback to get it from env $HOME", err))
			homeDir = os.Getenv("HOME")
		} else {
			homeDir = usr.HomeDir
		}
		kubeConfigPath = filepath.Join(homeDir, ".kube", "config")

		if _, err := os.Stat(kubeConfigPath); os.IsNotExist(err) {
			return nil, nil, errors.New("Couldn't find kubeconfig file. " +
				"Set the KUBECONFIG environment variable to your kubeconfig's path.")
		}
		loadingRules.ExplicitPath = kubeConfigPath
		console.Verbose(2, "Using kubeconfig from %q", kubeConfigPath)
	} else {
		console.Verbose(2, "Using kubeconfig from environment %q", kubeConfigPath)
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	if err != nil {
		return nil, nil, errors.Wrap(err, "Failed to build Kubernetes config")
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, errors.Wrap(err, "Failed to connect to Kubernetes")
	}

	return config, clientset, nil
}

// given a list of functions, this checks if the functions actually exist on the cluster
func CheckFunctionExistence(client client.Interface, functions []string, fnNamespace string) (err error) {
	fnMissing := make([]string, 0)
	for _, fnName := range functions {

		gvr, err := GetGVRFromAPIVersionKind(FISSION_API_VERSION, FISSION_FUNCTION)
		CheckError(err, "error finding GVR")

		resp, err := client.DynamicClient().Resource(*gvr).Namespace(fnNamespace).Get(context.TODO(), fnName, metav1.GetOptions{})
		CheckError(err, "error getting function")

		var fn *fv1.Function
		err = runtime.DefaultUnstructuredConverter.FromUnstructured(resp.UnstructuredContent(), &fn)
		CheckError(err, "error converting unstructured object to Environment")

		fnMissing = append(fnMissing, fnName)
	}

	if len(fnMissing) > 0 {
		return fmt.Errorf("function(s) %s, not present in namespace : %s", fnMissing, fnNamespace)
	}

	return nil
}

func GetVersion(client client.Interface) info.Versions {
	// Fetch client versions
	versions := info.Versions{
		Client: map[string]info.BuildMeta{
			"fission/core": info.BuildInfo(),
		},
	}

	for _, pmd := range plugin.FindAll() {
		versions.Client[pmd.Name] = info.BuildMeta{
			Version: pmd.Version,
		}
	}

	serverInfo, err := client.V1().Misc().ServerInfo()
	if err != nil {
		console.Warn(fmt.Sprintf("Error getting Fission API version: %v", err))
		serverInfo = &info.ServerInfo{}
	}

	// Fetch server versions
	versions.Server = map[string]info.BuildMeta{
		"fission/core": serverInfo.Build,
	}

	// FUTURE: fetch versions of plugins server-side

	return versions
}

func GetServer(input cli.Input) (c client.Interface, err error) {
	serverUrl, err := GetServerURL(input)
	if err != nil {
		return nil, err
	}

	// -- REMOVE --
	kubeConfigPath := os.Getenv("KUBECONFIG")
	if len(kubeConfigPath) == 0 {
		kubeConfigPath = filepath.Join(homedir.HomeDir(), ".kube", "config")
	}

	if _, err := os.Stat(kubeConfigPath); os.IsNotExist(err) {
		return nil, errors.New("--Couldn't find kubeconfig file. " +
			"Set the KUBECONFIG environment variable to your kubeconfig's path.")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	if err != nil {
		return nil, err
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return client.MakeClientset(rest.NewRESTClient(serverUrl), nil, dynamicClient), nil
}

func GetServerURL(input cli.Input) (serverUrl string, err error) {
	serverUrl = input.GlobalString(flagkey.Server)
	kubeContext := input.String(flagkey.KubeContext)
	if len(serverUrl) == 0 {
		// starts local portforwarder etc.
		serverUrl, err = GetApplicationUrl("application=fission-api", kubeContext)
		if err != nil {
			return "", err
		}
	}

	isHTTPS := strings.Index(serverUrl, "https://") == 0
	isHTTP := strings.Index(serverUrl, "http://") == 0

	if !(isHTTP || isHTTPS) {
		serverUrl = "http://" + serverUrl
	}

	return serverUrl, nil
}

func GetResourceReqs(input cli.Input, resReqs *apiv1.ResourceRequirements) (*apiv1.ResourceRequirements, error) {
	r := &apiv1.ResourceRequirements{}

	if resReqs != nil {
		r.Requests = resReqs.Requests
		r.Limits = resReqs.Limits
	}

	if len(r.Requests) == 0 {
		r.Requests = make(map[apiv1.ResourceName]resource.Quantity)
	}

	if len(r.Limits) == 0 {
		r.Limits = make(map[apiv1.ResourceName]resource.Quantity)
	}

	e := utils.MultiErrorWithFormat()

	if input.IsSet(flagkey.RuntimeMincpu) {
		mincpu := input.Int(flagkey.RuntimeMincpu)
		cpuRequest, err := resource.ParseQuantity(strconv.Itoa(mincpu) + "m")
		if err != nil {
			e = multierror.Append(e, errors.Wrap(err, "Failed to parse mincpu"))
		}
		r.Requests[apiv1.ResourceCPU] = cpuRequest
	}

	if input.IsSet(flagkey.RuntimeMinmemory) {
		minmem := input.Int(flagkey.RuntimeMinmemory)
		memRequest, err := resource.ParseQuantity(strconv.Itoa(minmem) + "Mi")
		if err != nil {
			e = multierror.Append(e, errors.Wrap(err, "Failed to parse minmemory"))
		}
		r.Requests[apiv1.ResourceMemory] = memRequest
	}

	if input.IsSet(flagkey.RuntimeMaxcpu) {
		maxcpu := input.Int(flagkey.RuntimeMaxcpu)
		cpuLimit, err := resource.ParseQuantity(strconv.Itoa(maxcpu) + "m")
		if err != nil {
			e = multierror.Append(e, errors.Wrap(err, "Failed to parse maxcpu"))
		}
		r.Limits[apiv1.ResourceCPU] = cpuLimit
	}

	if input.IsSet(flagkey.RuntimeMaxmemory) {
		maxmem := input.Int(flagkey.RuntimeMaxmemory)
		memLimit, err := resource.ParseQuantity(strconv.Itoa(maxmem) + "Mi")
		if err != nil {
			e = multierror.Append(e, errors.Wrap(err, "Failed to parse maxmemory"))
		}
		r.Limits[apiv1.ResourceMemory] = memLimit
	}

	limitCPU := r.Limits[apiv1.ResourceCPU]
	requestCPU := r.Requests[apiv1.ResourceCPU]

	if limitCPU.IsZero() && !requestCPU.IsZero() {
		r.Limits[apiv1.ResourceCPU] = requestCPU
	} else if limitCPU.Cmp(requestCPU) < 0 {
		e = multierror.Append(e, fmt.Errorf("MinCPU (%v) cannot be greater than MaxCPU (%v)", requestCPU.String(), limitCPU.String()))
	}

	limitMem := r.Limits[apiv1.ResourceMemory]
	requestMem := r.Requests[apiv1.ResourceMemory]

	if limitMem.IsZero() && !requestMem.IsZero() {
		r.Limits[apiv1.ResourceMemory] = requestMem
	} else if limitMem.Cmp(requestMem) < 0 {
		e = multierror.Append(e, fmt.Errorf("MinMemory (%v) cannot be greater than MaxMemory (%v)", requestMem.String(), limitMem.String()))
	}

	if e.ErrorOrNil() != nil {
		return nil, e
	}

	return &apiv1.ResourceRequirements{
		Requests: r.Requests,
		Limits:   r.Limits,
	}, nil
}

func GetSpecDir(input cli.Input) string {
	specDir := input.String(flagkey.SpecDir)
	if len(specDir) == 0 {
		specDir = "specs"
	}
	return specDir
}

func GetSpecIgnore(input cli.Input) string {
	specIgnoreFile := input.String(flagkey.SpecIgnore)
	if len(specIgnoreFile) == 0 {
		specIgnoreFile = SPEC_IGNORE_FILE
	}
	return specIgnoreFile
}

// GetSpecIgnoreParser reads the specignore file and returns the ignore.IgnoreParser
// if the specignore file does not exist it returns empty ignore.IgnoreParser
func GetSpecIgnoreParser(specDir, specIgnore string) (ignore.IgnoreParser, error) {

	specIgnorePath := filepath.Join(specDir, specIgnore)

	// check for existence of spec ignore file
	if _, err := os.Stat(specIgnorePath); errors.Is(err, os.ErrNotExist) {
		// return error if it's custom spec ignore file
		if specIgnore != SPEC_IGNORE_FILE {
			return nil, errors.Errorf("Spec ignore file '%s' doesn't exist. "+
				"Please check the file path: '%s'", specIgnore, specIgnorePath)
		}
		return ignore.CompileIgnoreLines(), nil
	}

	return ignore.CompileIgnoreFile(specIgnorePath)
}

func GetValidationFlag(input cli.Input) bool {
	validationFlag := input.String(flagkey.SpecValidate)
	// if flag has not been set, we return true to turn on validation by default
	if len(validationFlag) == 0 {
		return true
	}
	if validationFlag == "false" {
		return false
	}
	return true
}

// UpdateMapFromStringSlice parses key, val from "key=val" string array and updates passed map
func UpdateMapFromStringSlice(dataMap *map[string]string, params []string) bool {
	updated := false
	for _, m := range params {
		keyValue := strings.SplitN(m, "=", 2)
		if len(keyValue) == 2 {
			key := keyValue[0]
			value := keyValue[1]
			(*dataMap)[key] = value
			updated = true
		}
	}
	return updated
}

// GetEnvVarFromStringSlice parses key, val from "key=val" string array and updates passed []apiv1.EnvVar
func GetEnvVarFromStringSlice(params []string) []apiv1.EnvVar {
	envVarList := []apiv1.EnvVar{}
	for _, m := range params {
		keyValue := strings.SplitN(m, "=", 2)
		if len(keyValue) == 2 && keyValue[1] != "" {
			envVarList = append(envVarList, apiv1.EnvVar{
				Name:  keyValue[0],
				Value: keyValue[1],
			})
		}
	}
	return envVarList
}

func UrlForFunction(name, namespace string) string {
	prefix := "/fission-function"
	if namespace != metav1.NamespaceDefault {
		prefix = fmt.Sprintf("/fission-function/%s", namespace)
	}
	return fmt.Sprintf("%v/%v", prefix, name)
}

func ParseAnnotations(annotations []string) (map[string]string, error) {
	var invalidAnnotations string
	annotationMap := make(map[string]string)
	for _, arg := range annotations {
		if strings.Contains(arg, "=") && arg[0] != '=' {
			parts := strings.SplitN(arg, "=", 2)
			if len(parts) == 2 {
				annotationMap[parts[0]] = parts[1]
			} else {
				if invalidAnnotations != "" {
					invalidAnnotations = fmt.Sprintf("%s,%s", invalidAnnotations, arg)
				} else {
					invalidAnnotations = arg
				}
			}
		} else {
			if invalidAnnotations != "" {
				invalidAnnotations = fmt.Sprintf("%s,%s", invalidAnnotations, arg)
			} else {
				invalidAnnotations = arg
			}
		}
	}
	if invalidAnnotations != "" {
		return nil, errors.Errorf("invalid annotations: %s", invalidAnnotations)
	}
	return annotationMap, nil
}

func ApplyLabelsAndAnnotations(input cli.Input, objectMeta *metav1.ObjectMeta) error {
	labelStr := input.String(flagkey.Labels)
	if labelStr != "" {
		set, err := labels.ConvertSelectorToLabelsMap(labelStr)
		if err != nil {
			return err
		}
		objectMeta.Labels = set
	}
	annotationStr := input.StringSlice(flagkey.Annotation)
	if len(annotationStr) > 0 {
		set, err := ParseAnnotations(annotationStr)
		if err != nil {
			return err
		}
		objectMeta.Annotations = set
	}
	return nil
}

// GetGVRFromAPIVersionKind returns the GroupVersionResource for the APIVersion and Kind
func GetGVRFromAPIVersionKind(apiVersion, kind string) (*schema.GroupVersionResource, error) {
	kubeConfigPath := os.Getenv("KUBECONFIG")
	if len(kubeConfigPath) == 0 {
		kubeConfigPath = filepath.Join(homedir.HomeDir(), ".kube", "config")
	}

	if _, err := os.Stat(kubeConfigPath); os.IsNotExist(err) {
		return nil, errors.New("Couldn't find kubeconfig file. " +
			"Set the KUBECONFIG environment variable to your kubeconfig's path.")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	if err != nil {
		return nil, err
	}

	dc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))
	gvk := schema.FromAPIVersionAndKind(apiVersion, kind)
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, err
	}

	return &mapping.Resource, nil
}

func CheckError(err error, msg string) {
	colorReset := "\033[0m"
	colorRed := "\033[31m"
	errorPrefix := colorRed + "Error:" + colorReset

	if err != nil {
		if msg != "" {
			fmt.Println(errorPrefix, errors.Wrap(err, msg))
		} else {
			fmt.Println(errorPrefix, err)
		}
		os.Exit(1)
	}
}
