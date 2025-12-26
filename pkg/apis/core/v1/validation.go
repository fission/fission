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

package v1

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strings"

	"github.com/robfig/cron/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/fission/fission/pkg/mqtrigger/validator"
)

const (
	ErrorUnsupportedType = iota
	ErrorInvalidValue
	ErrorInvalidObject

	totalAnnotationSizeLimitB int = 256 * (1 << 10) // 256 kB
)

type (
	ValidationErrorType int

	// ValidationError is a custom error type for resource validation.
	// It indicate which field is invalid or illegal in the fission resource.
	// Also, it shows what kind of error type, bad value and detail error messages.
	ValidationError struct {
		// Type of validation error.
		// It indicates what kind of error of field in error output.
		Type ValidationErrorType

		// Name of error field.
		// Example: FunctionReference.Name
		Field string

		// Error field value.
		BadValue string

		// Detail error message
		Detail string
	}
)

func (e ValidationError) Error() string {
	// Example error message
	// Failed to create HTTP trigger: Invalid fission HTTPTrigger object:
	// * FunctionReference.Name: Invalid value: findped.ts: [...]

	errMsg := fmt.Sprintf("%v: ", e.Field)

	switch e.Type {
	case ErrorUnsupportedType:
		errMsg += fmt.Sprintf("Unsupported type: %v", e.BadValue)
	case ErrorInvalidValue:
		errMsg += fmt.Sprintf("Invalid value: %v", e.BadValue)
	case ErrorInvalidObject:
		errMsg += fmt.Sprintf("Invalid object: %v", e.BadValue)
	default:
		errMsg += fmt.Sprintf("Unknown error type: %v", e.BadValue)
	}

	if len(e.Detail) > 0 {
		errMsg += fmt.Sprintf(": %v", e.Detail)
	}

	return errMsg
}

func AggregateValidationErrors(objName string, err error) error {
	if err == nil {
		return nil
	}
	var errMsg bytes.Buffer
	errMsg.WriteString(fmt.Sprintf("Invalid fission %s objects:\n", objName))

	var unmaskError func(level int, err error)

	// Do nested error unwrapping
	unmaskError = func(level int, err error) {
		unwrapper, ok := err.(interface {
			Unwrap() []error
		})
		if ok {
			for _, e := range unwrapper.Unwrap() {
				unmaskError(level+1, e)
			}
		} else {
			if level > 0 {
				errMsg.WriteString(strings.Repeat("  ", level-1))
			}
			errMsg.WriteString(fmt.Sprintf("* %s\n", err.Error()))
		}
	}

	unmaskError(0, err)

	return errors.New(errMsg.String())
}

func MakeValidationErr(errType ValidationErrorType, field string, val interface{}, detail ...string) ValidationError {
	return ValidationError{
		Type:     errType,
		Field:    field,
		BadValue: fmt.Sprintf("%v", val),
		Detail:   fmt.Sprintf("%v", detail),
	}
}

func ValidateKubeLabel(field string, labels map[string]string) error {
	var errs error

	for k, v := range labels {
		// Example: XXX -> YYY
		// KubernetesWatchTriggerSpec.LabelSelector.Key: Invalid value: XXX
		// KubernetesWatchTriggerSpec.LabelSelector.Value: Invalid value: YYY
		errs = errors.Join(errs,
			MakeValidationErr(ErrorInvalidValue, fmt.Sprintf("%v.Key", field), k, validation.IsQualifiedName(k)...),
			MakeValidationErr(ErrorInvalidValue, fmt.Sprintf("%v.Value", field), v, validation.IsValidLabelValue(v)...))
	}

	return errs
}

func ValidateKubePort(field string, port int) error {
	var errs error

	e := validation.IsValidPortNum(port)
	if len(e) > 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field, port, e...))
	}
	return errs
}

func ValidateKubeName(field string, val string) error {
	var errs error

	e := validation.IsDNS1123Label(val)
	if len(e) > 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field, val, e...))
	}

	return errs
}

// validateNS is to match the k8s behaviour. Where it is not mandatory to provide a NS. And so we validate it if user has provided one.
// Or we skip the validation on namespace.
func validateNS(refName string, namespace string) error {
	if namespace != "" {
		return ValidateKubeName(refName, namespace)
	}
	return nil
}

func ValidateKubeReference(refName string, name string, namespace string) error {
	var errs error

	errs = errors.Join(errs,
		ValidateKubeName(fmt.Sprintf("%s.Name", refName), name),
		validateNS(fmt.Sprintf("%s.Namespace", refName), namespace))

	return errs
}

func IsValidCronSpec(spec string) error {
	cronSpecParser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	_, err := cronSpecParser.Parse(spec)
	return err
}

/* Resource validation function */

func (checksum Checksum) Validate() error {
	var errs error

	switch checksum.Type {
	case ChecksumTypeSHA256: // no op
	default:
		errs = errors.Join(errs, MakeValidationErr(ErrorUnsupportedType, "Checksum.Type", checksum.Type, "not a valid checksum type"))
	}

	return errs
}

func (archive Archive) Validate() error {
	var errs error

	if len(archive.Type) > 0 {
		switch archive.Type {
		case ArchiveTypeLiteral, ArchiveTypeUrl: // no op
		default:
			errs = errors.Join(errs, MakeValidationErr(ErrorUnsupportedType, "Archive.Type", archive.Type, "not a valid archive type"))
		}
	}

	if archive.Checksum != (Checksum{}) {
		errs = errors.Join(errs, archive.Checksum.Validate())
	}

	return errs
}

func (ref EnvironmentReference) Validate() error {
	return ValidateKubeReference("EnvironmentReference", ref.Name, ref.Namespace)
}

func (ref SecretReference) Validate() error {
	return ValidateKubeReference("SecretReference", ref.Name, ref.Namespace)
}

func (ref ConfigMapReference) Validate() error {
	return ValidateKubeReference("ConfigMapReference", ref.Name, ref.Namespace)
}

func (spec PackageSpec) Validate() error {
	var errs error

	errs = errors.Join(errs, spec.Environment.Validate())

	for _, r := range []Archive{spec.Source, spec.Deployment} {
		if len(r.URL) > 0 || len(r.Literal) > 0 {
			errs = errors.Join(errs, r.Validate())
		}
	}

	return errs
}

func (sts PackageStatus) Validate() error {
	var errs error

	switch sts.BuildStatus {
	case BuildStatusPending, BuildStatusRunning, BuildStatusSucceeded, BuildStatusFailed, BuildStatusNone: // no op
	default:
		errs = errors.Join(errs, MakeValidationErr(ErrorUnsupportedType, "PackageStatus.BuildStatus", sts.BuildStatus, "not a valid build status"))
	}

	return errs
}

func (ref PackageRef) Validate() error {
	return ValidateKubeReference("PackageRef", ref.Name, ref.Namespace)
}

func (ref FunctionPackageRef) Validate() error {
	return ref.PackageRef.Validate()
}

func (spec FunctionSpec) Validate() error {
	var errs error

	if spec.Environment != (EnvironmentReference{}) {
		errs = errors.Join(errs, spec.Environment.Validate())
	}

	if spec.Package != (FunctionPackageRef{}) {
		errs = errors.Join(errs, spec.Package.Validate())
	}

	for _, s := range spec.Secrets {
		errs = errors.Join(errs, s.Validate())
	}
	for _, c := range spec.ConfigMaps {
		errs = errors.Join(errs, c.Validate())
	}

	if !reflect.DeepEqual(spec.InvokeStrategy, InvokeStrategy{}) {
		errs = errors.Join(errs, spec.InvokeStrategy.Validate())
	}

	if spec.InvokeStrategy.ExecutionStrategy.ExecutorType == ExecutorTypeContainer && spec.PodSpec == nil {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidObject, "FunctionSpec.PodSpec", "", "executor type container requires a pod spec"))
	}

	// TODO Add below validation warning
	// if spec.FunctionTimeout <= 0 {
	// 	errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionTimeout value", spec.FunctionTimeout, "not a valid value. Should always be more than 0"))
	// }

	return errs
}

func (is InvokeStrategy) Validate() error {
	var errs error

	switch is.StrategyType {
	case StrategyTypeExecution: // no op
	default:
		errs = errors.Join(errs, MakeValidationErr(ErrorUnsupportedType, "InvokeStrategy.StrategyType", is.StrategyType, "not a valid strategy"))
	}

	errs = errors.Join(errs, is.ExecutionStrategy.Validate())

	return errs
}

func (es ExecutionStrategy) Validate() error {
	var errs error
	switch es.ExecutorType {
	case ExecutorTypeNewdeploy, ExecutorTypePoolmgr, ExecutorTypeContainer: // no op
	default:
		errs = errors.Join(errs, MakeValidationErr(ErrorUnsupportedType, "ExecutionStrategy.ExecutorType", es.ExecutorType, "not a valid executor type"))
	}

	if es.ExecutorType == ExecutorTypeNewdeploy || es.ExecutorType == ExecutorTypeContainer {
		if es.MinScale < 0 {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "ExecutionStrategy.MinScale", es.MinScale, "minimum scale must be greater than or equal to 0"))
		}

		if es.MaxScale <= 0 {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "ExecutionStrategy.MaxScale", es.MaxScale, "maximum scale must be greater than 0"))
		}

		if es.MaxScale < es.MinScale {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "ExecutionStrategy.MaxScale", es.MaxScale, "maximum scale must be greater than or equal to minimum scale"))
		}

		if es.TargetCPUPercent < 0 || es.TargetCPUPercent > 100 {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "ExecutionStrategy.TargetCPUPercent", es.TargetCPUPercent, "TargetCPUPercent must be a value between 1 - 100"))
		}

		// TODO Add validation warning
		// if es.SpecializationTimeout < 120 {
		//	errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "ExecutionStrategy.SpecializationTimeout", es.SpecializationTimeout, "SpecializationTimeout must be a value equal to or greater than 120"))
		//}
	}

	return errs
}

func (ref FunctionReference) Validate() error {
	var errs error

	switch ref.Type {
	case FunctionReferenceTypeFunctionName: // no op
	case FunctionReferenceTypeFunctionWeights: // no op
	default:
		errs = errors.Join(errs, MakeValidationErr(ErrorUnsupportedType, "FunctionReference.Type", ref.Type, "not a valid function reference type"))
	}

	if ref.Type == FunctionReferenceTypeFunctionName {
		errs = errors.Join(errs, ValidateKubeName("FunctionReference.Name", ref.Name))
	}

	return errs
}

func (runtime Runtime) Validate() error {
	var errs error

	if runtime.LoadEndpointPort > 0 {
		errs = errors.Join(errs, ValidateKubePort("Runtime.LoadEndpointPort", int(runtime.LoadEndpointPort)))
	}

	if runtime.FunctionEndpointPort > 0 {
		errs = errors.Join(errs, ValidateKubePort("Runtime.FunctionEndpointPort", int(runtime.FunctionEndpointPort)))
	}

	return errs
}

func (builder Builder) Validate() error {
	// do nothing for now
	return nil
}

func (spec EnvironmentSpec) Validate() error {
	var errs error

	if spec.Version < 1 || spec.Version > 3 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "EnvironmentSpec.Version", spec.Version, "not a valid environment version"))
	}

	errs = errors.Join(errs, spec.Runtime.Validate())

	if spec.Builder != (Builder{}) {
		errs = errors.Join(errs, spec.Builder.Validate())
	}

	if len(spec.AllowedFunctionsPerContainer) > 0 {
		switch spec.AllowedFunctionsPerContainer {
		case AllowedFunctionsPerContainerSingle, AllowedFunctionsPerContainerInfinite: // no op
		default:
			errs = errors.Join(errs, MakeValidationErr(ErrorUnsupportedType, "EnvironmentSpec.AllowedFunctionsPerContainer", spec.AllowedFunctionsPerContainer, "not a valid value"))
		}
	}

	if spec.Poolsize < 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "EnvironmentSpec.Poolsize", spec.Poolsize, "must be greater than or equal to 0"))
	}

	if spec.TerminationGracePeriod < 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "EnvironmentSpec.TerminationGracePeriod", spec.TerminationGracePeriod, "must be greater than or equal to 0"))
	}

	return errs
}

func (spec HTTPTriggerSpec) Validate() error {
	var errs error

	checkMethod := func(method string, errs error) error {
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
			http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace: // no op
		default:
			errs = errors.Join(errs, MakeValidationErr(ErrorUnsupportedType, "HTTPTriggerSpec.Method", spec.Method, "not a valid HTTP method"))
		}
		return errs
	}
	if len(spec.Methods) > 0 {
		for _, method := range spec.Methods {
			errs = checkMethod(method, errs)
		}
	}

	if len(spec.Method) > 0 {
		errs = checkMethod(spec.Method, errs)
	}

	errs = errors.Join(errs, spec.FunctionReference.Validate())

	if len(spec.Host) > 0 {
		e := validation.IsDNS1123Subdomain(spec.Host)
		if len(e) > 0 {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.Host", spec.Host, e...))
		}
	}

	errs = errors.Join(errs, spec.IngressConfig.Validate())
	return errs
}

func (config IngressConfig) Validate() error {
	var errs error

	// Details for how to validate Ingress host rule,
	// see https://github.com/kubernetes/kubernetes/blob/release-1.16/pkg/apis/networking/validation/validation.go

	if len(config.Path) > 0 {
		if !strings.HasPrefix(config.Path, "/") {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.IngressConfig.IngressRule.Path", config.Path, "must be an absolute path"))
		}

		_, err := regexp.CompilePOSIX(config.Path)
		if err != nil {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.IngressConfig.IngressRule.Path", config.Path, "must be a valid regex"))
		}
	}

	// In Ingress, to accept requests from all host, the host field will
	// be an empty string instead of "*" shown in kubectl. The router replaces
	// the asterisk with "" when creating/updating the Ingress, so here we
	// skip the check if the Host is equal to "*".
	if len(config.Host) > 0 && config.Host != "*" {
		if strings.Contains(config.Host, "*") {
			for _, msg := range validation.IsWildcardDNS1123Subdomain(config.Host) {
				errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.IngressConfig.IngressRule.Host", config.Host, msg))
			}
		}
		for _, msg := range validation.IsDNS1123Subdomain(config.Host) {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.IngressConfig.IngressRule.Host", config.Host, msg))
		}
	}

	// Details for how to validate annotations,
	// see https://github.com/kubernetes/kubernetes/blob/512eccac1f1d72d6d1cb304bc565c50d1f2e295e/staging/src/k8s.io/apimachinery/pkg/api/validation/objectmeta.go#L46

	var totalSize int64
	for k, v := range config.Annotations {
		for _, msg := range validation.IsQualifiedName(strings.ToLower(k)) {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.IngressConfig.Annotations.key", k, msg))
		}
		totalSize += (int64)(len(k)) + (int64)(len(v))
	}
	if totalSize > (int64)(totalAnnotationSizeLimitB) {
		msg := fmt.Sprintf("must have at most %v characters", totalSize)
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.IngressConfig.Annotations.value", totalAnnotationSizeLimitB, msg))
	}

	return errs
}

func (spec KubernetesWatchTriggerSpec) Validate() error {
	var errs error

	switch strings.ToUpper(spec.Type) {
	case "POD", "SERVICE", "REPLICATIONCONTROLLER", "JOB":
	default:
		errs = errors.Join(errs, MakeValidationErr(ErrorUnsupportedType, "KubernetesWatchTriggerSpec.Type", spec.Type, "not a valid supported type"))
	}

	errs = errors.Join(errs,
		ValidateKubeName("KubernetesWatchTriggerSpec.Namespace", spec.Namespace),
		ValidateKubeLabel("KubernetesWatchTriggerSpec.LabelSelector", spec.LabelSelector),
		spec.FunctionReference.Validate())

	return errs
}

func (spec MessageQueueTriggerSpec) Validate() error {
	var errs error

	errs = errors.Join(errs, spec.FunctionReference.Validate())

	if !validator.IsValidMessageQueue((string)(spec.MessageQueueType), spec.MqtKind) {
		errs = errors.Join(errs, MakeValidationErr(ErrorUnsupportedType, "MessageQueueTriggerSpec.MessageQueueType", spec.MessageQueueType, "not a supported message queue type"))
	} else {
		if !validator.IsValidTopic((string)(spec.MessageQueueType), spec.Topic, spec.MqtKind) {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "MessageQueueTriggerSpec.Topic", spec.Topic, "not a valid topic"))
		}

		if len(spec.ResponseTopic) > 0 && !validator.IsValidTopic((string)(spec.MessageQueueType), spec.ResponseTopic, spec.MqtKind) {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "MessageQueueTriggerSpec.ResponseTopic", spec.ResponseTopic, "not a valid topic"))
		}
	}

	return errs
}

func (spec TimeTriggerSpec) Validate() error {
	var errs error

	err := IsValidCronSpec(spec.Cron)
	if err != nil {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "TimeTriggerSpec.Cron", spec.Cron, "not a valid cron spec"))
	}

	errs = errors.Join(errs, spec.FunctionReference.Validate())

	return errs
}

func validateMetadata(field string, m metav1.ObjectMeta) error {
	return ValidateKubeReference(field, m.Name, m.Namespace)
}

func (p *Package) Validate() error {
	var errs error

	errs = errors.Join(errs,
		validateMetadata("Package", p.ObjectMeta),
		p.Spec.Validate(),
		p.Status.Validate())

	return errs
}

func (pl *PackageList) Validate() error {
	var errs error
	// not validate ListMeta
	for _, p := range pl.Items {
		errs = errors.Join(errs, p.Validate())
	}
	return errs
}

func (f *Function) Validate() error {
	var errs error

	errs = errors.Join(errs,
		validateMetadata("Function", f.ObjectMeta),
		f.Spec.Validate())

	return errs
}

func (fl *FunctionList) Validate() error {
	var errs error
	for _, f := range fl.Items {
		errs = errors.Join(errs, f.Validate())
	}
	return errs
}

func (e *Environment) Validate() error {
	var errs error

	errs = errors.Join(errs,
		validateMetadata("Environment", e.ObjectMeta),
		e.Spec.Validate())

	if e.Spec.Runtime.PodSpec != nil {
		for _, container := range e.Spec.Runtime.PodSpec.Containers {
			if container.Command == nil && container.Image == e.Spec.Runtime.Image && container.Name != e.Name {
				errs = errors.Join(errs, errors.New("container with image same as runtime image in podspec, must have name same as environment name"))
			}
		}
	}
	return errs
}

func (el *EnvironmentList) Validate() error {
	var errs error
	for _, e := range el.Items {
		errs = errors.Join(errs, e.Validate())
	}
	return errs
}

func (h *HTTPTrigger) Validate() error {
	var errs error

	errs = errors.Join(errs,
		validateMetadata("HTTPTrigger", h.ObjectMeta),
		h.Spec.Validate())

	return errs
}

func (hl *HTTPTriggerList) Validate() error {
	var errs error
	for _, h := range hl.Items {
		errs = errors.Join(errs, h.Validate())
	}
	return errs
}

func (k *KubernetesWatchTrigger) Validate() error {
	var errs error

	errs = errors.Join(errs,
		validateMetadata("KubernetesWatchTrigger", k.ObjectMeta),
		k.Spec.Validate())

	return errs
}

func (kl *KubernetesWatchTriggerList) Validate() error {
	var errs error
	for _, k := range kl.Items {
		errs = errors.Join(errs, k.Validate())
	}
	return errs
}

func (t *TimeTrigger) Validate() error {
	var errs error

	errs = errors.Join(errs,
		validateMetadata("TimeTrigger", t.ObjectMeta),
		t.Spec.Validate())

	return errs
}

func (tl *TimeTriggerList) Validate() error {
	var errs error
	for _, t := range tl.Items {
		errs = errors.Join(errs, t.Validate())
	}
	return errs
}

func (m *MessageQueueTrigger) Validate() error {
	var errs error

	errs = errors.Join(errs,
		validateMetadata("MessageQueueTrigger", m.ObjectMeta),
		m.Spec.Validate())

	return errs
}

func (ml *MessageQueueTriggerList) Validate() error {
	var errs error
	for _, m := range ml.Items {
		errs = errors.Join(errs, m.Validate())
	}
	return errs
}
