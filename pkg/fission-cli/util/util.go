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
	"io"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
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

func GetApplicationUrl(ctx context.Context, selector string, kubeContext string) (string, error) {
	var serverUrl string
	// Use FISSION_URL env variable if set; otherwise, port-forward to controller.
	fissionUrl := os.Getenv("FISSION_URL")
	if len(fissionUrl) == 0 {
		fissionNamespace := GetFissionNamespace()
		localPort, err := SetupPortForward(ctx, fissionNamespace, selector, kubeContext)
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

func getLoadingRules() (loadingRules *clientcmd.ClientConfigLoadingRules, err error) {
	loadingRules = clientcmd.NewDefaultClientConfigLoadingRules()

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
			return nil, errors.New("Couldn't find kubeconfig file. " +
				"Set the KUBECONFIG environment variable to your kubeconfig's path.")
		}
		loadingRules.ExplicitPath = kubeConfigPath
		console.Verbose(2, "Using kubeconfig from %q", kubeConfigPath)
	} else {
		console.Verbose(2, "Using kubeconfig from environment %q", kubeConfigPath)
	}
	return loadingRules, nil
}

// GetKubernetesClient builds a new kubernetes client. If the KUBECONFIG
// environment variable is empty or doesn't exist, ~/.kube/config is used for
// the kube config path
func GetKubernetesClient(kubeContext string) (*restclient.Config, kubernetes.Interface, error) {
	loadingRules, err := getLoadingRules()
	if err != nil {
		return nil, nil, err
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

// GetKubernetesNamespace builds a new kubernetes client. If the KUBECONFIG
// environment variable is empty or doesn't exist, ~/.kube/config is used for
// the kube config path
func GetKubernetesNamespace(kubeContext string) (currentNS string, err error) {
	loadingRules, err := getLoadingRules()
	if err != nil {
		return "", err
	}

	namespace, _, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).Namespace()
	if err != nil {
		return "", errors.Wrap(err, "Failed to build Kubernetes config")
	}

	return namespace, nil
}

// given a list of functions, this checks if the functions actually exist on the cluster
func CheckFunctionExistence(ctx context.Context, client cmd.Client, functions []string, fnNamespace string) (err error) {
	fnMissing := make([]string, 0)
	for _, fnName := range functions {
		_, err := client.FissionClientSet.CoreV1().Functions(fnNamespace).Get(ctx, fnName, metav1.GetOptions{})
		if err != nil {
			fnMissing = append(fnMissing, fnName)
		}
	}

	if len(fnMissing) > 0 {
		return fmt.Errorf("function(s) %s, not present in namespace : %s", fnMissing, fnNamespace)
	}

	return nil
}

func GetVersion(ctx context.Context, client cmd.Client) info.Versions {
	// Fetch client versions
	versions := info.Versions{
		Client: map[string]info.BuildMeta{
			"fission/core": info.BuildInfo(),
		},
	}

	for _, pmd := range plugin.FindAll(ctx) {
		versions.Client[pmd.Name] = info.BuildMeta{
			Version: pmd.Version,
		}
	}

	// TODO: verify it
	serverInfo := GetServerInfo()

	// Fetch server versions
	versions.Server = map[string]info.BuildMeta{
		"fission/core": serverInfo.Build,
	}

	// FUTURE: fetch versions of plugins server-side

	return versions
}

func GetServerInfo() info.ServerInfo {
	return info.ApiInfo()
}

func GetServerURL(input cli.Input) (serverUrl string, err error) {
	serverUrl = input.GlobalString(flagkey.Server)
	kubeContext := input.String(flagkey.KubeContext)
	if len(serverUrl) == 0 {
		// starts local portforwarder etc.
		serverUrl, err = GetApplicationUrl(input.Context(), "application=fission-api", kubeContext)
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

func GetResourceReqs(input cli.Input, resReqs *v1.ResourceRequirements) (*v1.ResourceRequirements, error) {
	r := &v1.ResourceRequirements{}

	if resReqs != nil {
		r.Requests = resReqs.Requests
		r.Limits = resReqs.Limits
	}

	if len(r.Requests) == 0 {
		r.Requests = make(map[v1.ResourceName]resource.Quantity)
	}

	if len(r.Limits) == 0 {
		r.Limits = make(map[v1.ResourceName]resource.Quantity)
	}

	e := utils.MultiErrorWithFormat()

	if input.IsSet(flagkey.RuntimeMincpu) {
		mincpu := input.Int(flagkey.RuntimeMincpu)
		cpuRequest, err := resource.ParseQuantity(strconv.Itoa(mincpu) + "m")
		if err != nil {
			e = multierror.Append(e, errors.Wrap(err, "Failed to parse mincpu"))
		}
		r.Requests[v1.ResourceCPU] = cpuRequest
	}

	if input.IsSet(flagkey.RuntimeMinmemory) {
		minmem := input.Int(flagkey.RuntimeMinmemory)
		memRequest, err := resource.ParseQuantity(strconv.Itoa(minmem) + "Mi")
		if err != nil {
			e = multierror.Append(e, errors.Wrap(err, "Failed to parse minmemory"))
		}
		r.Requests[v1.ResourceMemory] = memRequest
	}

	if input.IsSet(flagkey.RuntimeMaxcpu) {
		maxcpu := input.Int(flagkey.RuntimeMaxcpu)
		cpuLimit, err := resource.ParseQuantity(strconv.Itoa(maxcpu) + "m")
		if err != nil {
			e = multierror.Append(e, errors.Wrap(err, "Failed to parse maxcpu"))
		}
		r.Limits[v1.ResourceCPU] = cpuLimit
	}

	if input.IsSet(flagkey.RuntimeMaxmemory) {
		maxmem := input.Int(flagkey.RuntimeMaxmemory)
		memLimit, err := resource.ParseQuantity(strconv.Itoa(maxmem) + "Mi")
		if err != nil {
			e = multierror.Append(e, errors.Wrap(err, "Failed to parse maxmemory"))
		}
		r.Limits[v1.ResourceMemory] = memLimit
	}

	limitCPU := r.Limits[v1.ResourceCPU]
	requestCPU := r.Requests[v1.ResourceCPU]

	if limitCPU.IsZero() && !requestCPU.IsZero() {
		r.Limits[v1.ResourceCPU] = requestCPU
	} else if limitCPU.Cmp(requestCPU) < 0 {
		e = multierror.Append(e, fmt.Errorf("MinCPU (%v) cannot be greater than MaxCPU (%v)", requestCPU.String(), limitCPU.String()))
	}

	limitMem := r.Limits[v1.ResourceMemory]
	requestMem := r.Requests[v1.ResourceMemory]

	if limitMem.IsZero() && !requestMem.IsZero() {
		r.Limits[v1.ResourceMemory] = requestMem
	} else if limitMem.Cmp(requestMem) < 0 {
		e = multierror.Append(e, fmt.Errorf("MinMemory (%v) cannot be greater than MaxMemory (%v)", requestMem.String(), limitMem.String()))
	}

	if e.ErrorOrNil() != nil {
		return nil, e
	}

	return &v1.ResourceRequirements{
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

// GetEnvVarFromStringSlice parses key, val from "key=val" string array and updates passed []v1.EnvVar
func GetEnvVarFromStringSlice(params []string) []v1.EnvVar {
	envVarList := []v1.EnvVar{}
	for _, m := range params {
		keyValue := strings.SplitN(m, "=", 2)
		if len(keyValue) == 2 && keyValue[1] != "" {
			envVarList = append(envVarList, v1.EnvVar{
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

func GetStorageURL(ctx context.Context, kubeContext string) (*url.URL, error) {
	storageLocalPort, err := SetupPortForward(ctx, GetFissionNamespace(), "application=fission-storage", kubeContext)
	if err != nil {
		return nil, err
	}

	serverURL, err := url.Parse("http://127.0.0.1:" + storageLocalPort)
	if err != nil {
		return nil, err
	}

	return serverURL, nil
}

func GetResourceNamespace(input cli.Input, deprecatedFlag string) (namespace, currentNS string, err error) {
	namespace = input.String(deprecatedFlag)
	currentNS = namespace

	if input.String(flagkey.Namespace) != "" {
		namespace = input.String(flagkey.Namespace)
		currentNS = namespace
		console.Verbose(2, "Namespace for resource %s ", currentNS)
		return namespace, currentNS, err
	}

	if namespace == "" {
		if os.Getenv("FISSION_DEFAULT_NAMESPACE") != "" {
			currentNS = os.Getenv("FISSION_DEFAULT_NAMESPACE")
		} else {
			kubeContext := input.String(flagkey.KubeContext)
			currentNS, err = GetKubernetesNamespace(kubeContext)
			if err != nil {
				return namespace, currentNS, err
			}
		}
	}

	console.Verbose(2, "Namespace for resource %s ", currentNS)

	return namespace, currentNS, nil
}

// CheckHTTPTriggerDuplicates checks whether the tuple (Method, Host, URL) is duplicate or not.
func CheckHTTPTriggerDuplicates(ctx context.Context, client cmd.Client, t *fv1.HTTPTrigger) error {
	triggers, err := client.FissionClientSet.CoreV1().HTTPTriggers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, ht := range triggers.Items {
		if ht.ObjectMeta.UID == t.ObjectMeta.UID {
			// Same resource. No need to check.
			continue
		}
		urlMatch := false
		if (ht.Spec.RelativeURL != "" && ht.Spec.RelativeURL == t.Spec.RelativeURL) || (ht.Spec.Prefix != nil && t.Spec.Prefix != nil && *ht.Spec.Prefix != "" && *ht.Spec.Prefix == *t.Spec.Prefix) {
			urlMatch = true
		}
		methodMatch := false
		if ht.Spec.Method == t.Spec.Method && len(ht.Spec.Methods) == len(t.Spec.Methods) {
			methodMatch = true
			sort.Strings(ht.Spec.Methods)
			sort.Strings(t.Spec.Methods)
			for i, m1 := range ht.Spec.Methods {
				if m1 != t.Spec.Methods[i] {
					methodMatch = false
				}
			}
		}
		if urlMatch && methodMatch && ht.Spec.Method == t.Spec.Method && ht.Spec.Host == t.Spec.Host {
			return fmt.Errorf("HTTPTrigger with same Host, URL & method already exists (%v)",
				ht.ObjectMeta.Name)
		}
	}
	return nil
}

func SecretExists(ctx context.Context, m *metav1.ObjectMeta, kClient kubernetes.Interface) error {

	_, err := kClient.CoreV1().Secrets(m.Namespace).Get(ctx, m.Name, metav1.GetOptions{})
	return err
}

func ConfigMapExists(ctx context.Context, m *metav1.ObjectMeta, kClient kubernetes.Interface) error {

	_, err := kClient.CoreV1().ConfigMaps(m.Namespace).Get(ctx, m.Name, metav1.GetOptions{})
	return err
}

func GetSvcName(ctx context.Context, kClient kubernetes.Interface, application string) (string, error) {
	var podNamespace = os.Getenv("POD_NAMESPACE")
	if podNamespace == "" {
		podNamespace = "fission"
	}

	appLabelSelector := "application=" + application

	services, err := kClient.CoreV1().Services(podNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: appLabelSelector,
	})
	if err != nil {
		return "", err
	}

	if len(services.Items) > 1 || len(services.Items) == 0 {
		return "", errors.Errorf("more than one service found for application=%s", application)
	}
	service := services.Items[0]
	return service.Name + "." + podNamespace, nil
}

// FunctionPodLogs : Get logs for a function directly from pod
func FunctionPodLogs(ctx context.Context, fnName, ns string, client cmd.Client) (err error) {

	podNs := "fission-function"

	if len(ns) == 0 {
		ns = metav1.NamespaceDefault
	} else if ns != metav1.NamespaceDefault {
		// TODO: does it remains the same now????
		// If the function namespace is "default", executor
		// will create function pods under "fission-function".
		// Otherwise, the function pod will be created under
		// the same namespace of function.
		podNs = ns
	}

	f, err := client.FissionClientSet.CoreV1().Functions(ns).Get(ctx, fnName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// Get function Pods first
	selector := map[string]string{
		fv1.FUNCTION_UID:          string(f.ObjectMeta.UID),
		fv1.ENVIRONMENT_NAME:      f.Spec.Environment.Name,
		fv1.ENVIRONMENT_NAMESPACE: f.Spec.Environment.Namespace,
	}
	podList, err := client.KubernetesClient.CoreV1().Pods(podNs).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(selector).AsSelector().String(),
	})
	if err != nil {
		return err
	}

	// Get the logs for last Pod executed
	pods := podList.Items
	sort.Slice(pods, func(i, j int) bool {
		rv1, _ := strconv.ParseInt(pods[i].ObjectMeta.ResourceVersion, 10, 32)
		rv2, _ := strconv.ParseInt(pods[j].ObjectMeta.ResourceVersion, 10, 32)
		return rv1 > rv2
	})

	if len(pods) <= 0 {
		return errors.New("no active pods found")

	}

	// get the pod with highest resource version
	err = getContainerLog(ctx, client.KubernetesClient, f, &pods[0])
	if err != nil {
		return errors.Wrapf(err, "error getting container logs")

	}
	return err
}

func getContainerLog(ctx context.Context, kubernetesClient kubernetes.Interface, fn *fv1.Function, pod *v1.Pod) (err error) {
	seq := strings.Repeat("=", 35)

	for _, container := range pod.Spec.Containers {
		podLogOpts := v1.PodLogOptions{Container: container.Name} // Only the env container, not fetcher
		podLogsReq := kubernetesClient.CoreV1().Pods(pod.Namespace).GetLogs(pod.ObjectMeta.Name, &podLogOpts)

		podLogs, err := podLogsReq.Stream(ctx)
		if err != nil {
			return errors.Wrapf(err, "error streaming pod log")
		}

		msg := fmt.Sprintf("\n%v\nFunction: %v\nEnvironment: %v\nNamespace: %v\nPod: %v\nContainer: %v\nNode: %v\n%v\n", seq,
			fn.ObjectMeta.Name, fn.Spec.Environment.Name, pod.Namespace, pod.Name, container.Name, pod.Spec.NodeName, seq)

		if _, err := io.WriteString(os.Stdout, msg); err != nil {
			return errors.Wrapf(err, "error copying pod log")
		}

		_, err = io.Copy(os.Stdout, podLogs)
		if err != nil {
			return errors.Wrapf(err, "error copying pod log")
		}

		podLogs.Close()
	}

	return nil
}
