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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"

	"errors"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/info"
	"github.com/fission/fission/pkg/plugin"
)

const (
	ENV_FISSION_NAMESPACE  string = "FISSION_NAMESPACE"
	ENV_FISSION_URL        string = "FISSION_URL"
	ENV_FISSION_AUTH_TOKEN string = "FISSION_AUTH_TOKEN"
	localhostURL           string = "http://127.0.0.1:"
	authHeader             string = "Authorization"
	tokenType              string = "Bearer"
)

func GetFissionNamespace() string {
	fissionNamespace := os.Getenv(ENV_FISSION_NAMESPACE)
	return fissionNamespace
}

func ResolveFunctionNS(namespace string) string {
	if namespace != metav1.NamespaceDefault {
		return namespace
	}
	if len(os.Getenv(ENV_FUNCTION_NAMESPACE)) > 0 {
		return os.Getenv(ENV_FUNCTION_NAMESPACE)
	}
	return namespace
}

func GetApplicationUrl(ctx context.Context, client cmd.Client, selector string) (string, error) {
	var serverUrl string
	// Use FISSION_URL env variable if set; otherwise, port-forward to controller.
	fissionUrl := os.Getenv(ENV_FISSION_URL)
	if len(fissionUrl) == 0 {
		fissionNamespace := GetFissionNamespace()
		localPort, err := SetupPortForward(ctx, client, fissionNamespace, selector)
		if err != nil {
			return "", err
		}
		serverUrl = fmt.Sprintf("%s%s", localhostURL, localPort)
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
	inv := regexp.MustCompile("[^-a-z0-9]")
	newName = string(inv.ReplaceAll([]byte(newName), []byte("-")))

	// trim leading non-alphabetic
	leadingnonalpha := regexp.MustCompile("^[^a-z]+")
	newName = string(leadingnonalpha.ReplaceAll([]byte(newName), []byte{}))

	// trim trailing
	trailing := regexp.MustCompile("[^a-z0-9]+$")
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

func GetVersion(ctx context.Context, input cli.Input, cmdClient cmd.Client) info.Versions {
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

	serverInfo := GetServerInfo(input, cmdClient)

	// Fetch server versions
	versions.Server = map[string]info.BuildMeta{
		"fission/core": serverInfo.Build,
	}

	// FUTURE: fetch versions of plugins server-side

	return versions
}

func GetServerInfo(input cli.Input, cmdClient cmd.Client) *info.ServerInfo {

	var serverInfo info.ServerInfo
	serverURL, err := GetRouterURL(input.Context(), cmdClient)
	if err != nil {
		console.Warn("could not connect to server")
		return &serverInfo
	}
	// make request
	req, err := http.NewRequestWithContext(input.Context(), "GET", fmt.Sprintf("%s%s", serverURL.String(), "/_version"), nil)
	if err != nil {
		console.Warn("could not create http request")
		return &serverInfo
	}

	req.Header.Add(authHeader, fmt.Sprintf("%s %s", tokenType, os.Getenv(ENV_FISSION_AUTH_TOKEN)))
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		console.Warn("could not get data from server")
		return &serverInfo
	}

	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		// display user a warning message to set environment variable FISSION_AUTH_TOKEN
		if len(os.Getenv(ENV_FISSION_AUTH_TOKEN)) <= 0 {
			console.Warn(fmt.Sprintf("Please consider setting %s as environment variable, if authentication is enabled", ENV_FISSION_AUTH_TOKEN))
		}
	}
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("HTTP error %v", resp.StatusCode)
		console.Warn(msg)
		return &serverInfo
	}

	err = json.NewDecoder(resp.Body).Decode(&serverInfo)
	if err != nil {
		console.Warn(fmt.Sprintf("Error getting Fission API version: %v", err))
		serverInfo = info.ServerInfo{}
	}

	return &serverInfo
}

func GetRouterURL(ctx context.Context, cmdClient cmd.Client) (serverURL *url.URL, err error) {
	routerURL := os.Getenv("FISSION_ROUTER_URL")
	if len(routerURL) > 0 {
		return url.Parse(routerURL)
	}

	// Portforward to the fission router
	localRouterPort, err := SetupPortForward(ctx, cmdClient, GetFissionNamespace(), "application=fission-router")
	if err != nil {
		return serverURL, err
	}

	serverURL, err = url.Parse(fmt.Sprintf("%s%s", localhostURL, localRouterPort))
	if err != nil {
		return serverURL, err
	}
	return serverURL, err
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

	var errs error

	if input.IsSet(flagkey.RuntimeMincpu) {
		mincpu := input.Int(flagkey.RuntimeMincpu)
		cpuRequest, err := resource.ParseQuantity(strconv.Itoa(mincpu) + "m")
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to parse mincpu: %w", err))
		}
		r.Requests[v1.ResourceCPU] = cpuRequest
	}

	if input.IsSet(flagkey.RuntimeMinmemory) {
		minmem := input.Int(flagkey.RuntimeMinmemory)
		memRequest, err := resource.ParseQuantity(strconv.Itoa(minmem) + "Mi")
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to parse minmemory: %w", err))
		}
		r.Requests[v1.ResourceMemory] = memRequest
	}

	if input.IsSet(flagkey.RuntimeMaxcpu) {
		maxcpu := input.Int(flagkey.RuntimeMaxcpu)
		cpuLimit, err := resource.ParseQuantity(strconv.Itoa(maxcpu) + "m")
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to parse maxcpu: %w", err))
		}
		r.Limits[v1.ResourceCPU] = cpuLimit
	}

	if input.IsSet(flagkey.RuntimeMaxmemory) {
		maxmem := input.Int(flagkey.RuntimeMaxmemory)
		memLimit, err := resource.ParseQuantity(strconv.Itoa(maxmem) + "Mi")
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to parse maxmemory: %w", err))
		}
		r.Limits[v1.ResourceMemory] = memLimit
	}

	limitCPU := r.Limits[v1.ResourceCPU]
	requestCPU := r.Requests[v1.ResourceCPU]

	if limitCPU.IsZero() && !requestCPU.IsZero() {
		r.Limits[v1.ResourceCPU] = requestCPU
	} else if limitCPU.Cmp(requestCPU) < 0 {
		errs = errors.Join(errs, fmt.Errorf("minCPU (%v) cannot be greater than MaxCPU (%v)", requestCPU.String(), limitCPU.String()))
	}

	limitMem := r.Limits[v1.ResourceMemory]
	requestMem := r.Requests[v1.ResourceMemory]

	if limitMem.IsZero() && !requestMem.IsZero() {
		r.Limits[v1.ResourceMemory] = requestMem
	} else if limitMem.Cmp(requestMem) < 0 {
		errs = errors.Join(errs, fmt.Errorf("minMemory (%v) cannot be greater than MaxMemory (%v)", requestMem.String(), limitMem.String()))
	}

	if errs != nil {
		return nil, errs
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
			return nil, fmt.Errorf("spec ignore file '%s' doesn't exist. "+
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
		return nil, fmt.Errorf("invalid annotations: %s", invalidAnnotations)
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

func GetStorageURL(ctx context.Context, client cmd.Client) (*url.URL, error) {
	storagesvcURL := os.Getenv("FISSION_STORAGESVC_URL")
	if len(storagesvcURL) > 0 {
		return url.Parse(storagesvcURL)
	}
	storageLocalPort, err := SetupPortForward(ctx, client, GetFissionNamespace(), "application=fission-storage")
	if err != nil {
		return nil, err
	}

	serverURL, err := url.Parse(fmt.Sprintf("%s%s", localhostURL, storageLocalPort))
	if err != nil {
		return nil, err
	}

	return serverURL, nil
}

// CheckHTTPTriggerDuplicates checks whether the tuple (Method, Host, URL) is duplicate or not.
func CheckHTTPTriggerDuplicates(ctx context.Context, client cmd.Client, t *fv1.HTTPTrigger) error {
	triggers, err := client.FissionClientSet.CoreV1().HTTPTriggers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, ht := range triggers.Items {
		if k8sCache.MetaObjectToName(&ht.ObjectMeta).String() == k8sCache.MetaObjectToName(&t.ObjectMeta).String() {
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
				ht.Name)
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
	appLabelSelector := "application=" + application

	services, err := kClient.CoreV1().Services("").List(ctx, metav1.ListOptions{
		LabelSelector: appLabelSelector,
	})
	if err != nil {
		return "", err
	}

	if len(services.Items) > 1 || len(services.Items) == 0 {
		return "", fmt.Errorf("more than one service found for application=%s", application)
	}
	service := services.Items[0]
	return service.Name + "." + service.Namespace, nil
}

// FunctionPodLogs : Get logs for a function directly from pod
func FunctionPodLogs(ctx context.Context, fnName, ns string, client cmd.Client) (err error) {

	if len(ns) == 0 {
		ns = metav1.NamespaceDefault
	}

	f, err := client.FissionClientSet.CoreV1().Functions(ns).Get(ctx, fnName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// Get function Pods first
	var selector map[string]string
	if f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeContainer {
		selector = map[string]string{
			fv1.FUNCTION_UID:          string(f.UID),
			fv1.ENVIRONMENT_NAME:      f.Spec.Environment.Name,
			fv1.ENVIRONMENT_NAMESPACE: f.Spec.Environment.Namespace,
		}
	} else {
		selector = map[string]string{
			fv1.FUNCTION_UID: string(f.UID),
		}
	}
	podList, err := client.KubernetesClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(selector).AsSelector().String(),
	})
	if err != nil {
		return err
	}

	// Get the logs for last Pod executed
	pods := podList.Items
	sort.Slice(pods, func(i, j int) bool {
		rv1, _ := strconv.ParseInt(pods[i].ResourceVersion, 10, 32)
		rv2, _ := strconv.ParseInt(pods[j].ResourceVersion, 10, 32)
		return rv1 > rv2
	})

	if len(pods) <= 0 {
		return errors.New("no active pods found for function in namespace " + ns)
	}

	// get the pod with highest resource version
	err = getContainerLog(ctx, client.KubernetesClient, f, &pods[0])
	if err != nil {
		return fmt.Errorf("error getting container logs: %w", err)

	}
	return err
}

func getContainerLog(ctx context.Context, kubernetesClient kubernetes.Interface, fn *fv1.Function, pod *v1.Pod) (err error) {
	seq := strings.Repeat("=", 35)

	for _, container := range pod.Spec.Containers {
		podLogOpts := v1.PodLogOptions{Container: container.Name} // Only the env container, not fetcher
		podLogsReq := kubernetesClient.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &podLogOpts)

		podLogs, err := podLogsReq.Stream(ctx)
		if err != nil {
			return fmt.Errorf("error streaming pod log: %w", err)
		}

		msg := fmt.Sprintf("\n%v\nFunction: %v\nEnvironment: %v\nNamespace: %v\nPod: %v\nContainer: %v\nNode: %v\n%v\n", seq,
			fn.Name, fn.Spec.Environment.Name, pod.Namespace, pod.Name, container.Name, pod.Spec.NodeName, seq)

		if _, err := io.WriteString(os.Stdout, msg); err != nil {
			return fmt.Errorf("error copying pod log: %w", err)
		}

		_, err = io.Copy(os.Stdout, podLogs)
		if err != nil {
			return fmt.Errorf("error copying pod log: %w", err)
		}

		podLogs.Close()
	}

	return nil
}
