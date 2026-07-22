// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

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
	fmt.Fprintf(&errMsg, "Invalid fission %s objects:\n", objName)

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
			fmt.Fprintf(&errMsg, "* %v\n", err.Error())
		}
	}

	unmaskError(0, err)

	return errors.New(errMsg.String())
}

func MakeValidationErr(errType ValidationErrorType, field string, val any, detail ...string) ValidationError {
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

// ociDigestRegexp matches the only digest form OCIArchive.Digest accepts;
// it mirrors the field's kubebuilder Pattern marker in types.go.
var ociDigestRegexp = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func (o OCIArchive) Validate() error {
	var errs error

	if len(o.Image) == 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "OCIArchive.Image", o.Image, "image reference is required"))
	}

	if len(o.Digest) > 0 && !ociDigestRegexp.MatchString(o.Digest) {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "OCIArchive.Digest", o.Digest, "must be 'sha256:' followed by 64 hex characters"))
	}

	// A digest embedded in the image reference and a Digest field would race
	// for precedence (the pull paths would resolve them differently) — make
	// the user pick one.
	if len(o.Digest) > 0 && strings.Contains(o.Image, "@") {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "OCIArchive.Digest", o.Digest, "image reference already pins a digest; set the digest in one place only"))
	}

	// SubPath rides into pod volumeMount subPaths on the image-volume path,
	// where Kubernetes rejects absolute paths and path traversal.
	if cleaned := strings.Trim(o.SubPath, "/"); cleaned != "" {
		if strings.HasPrefix(o.SubPath, "/") || cleaned != filepath.Clean(cleaned) || strings.Contains(cleaned, "..") {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "OCIArchive.SubPath", o.SubPath, "must be a clean relative directory path inside the image (no leading '/', no '..')"))
		}
	}

	return errs
}

func (archive Archive) Validate() error {
	var errs error

	if len(archive.Type) > 0 {
		switch archive.Type {
		case ArchiveTypeLiteral, ArchiveTypeUrl, ArchiveTypeOCI: // no op
		default:
			errs = errors.Join(errs, MakeValidationErr(ErrorUnsupportedType, "Archive.Type", archive.Type, "not a valid archive type"))
		}
	}

	// At most one content source. The Archive CEL rule only covers url+oci
	// (it cannot reference the byte-format literal field — see the marker
	// comment in types.go); combinations involving literal are rejected here,
	// via the validating webhook.
	set := 0
	if len(archive.Literal) > 0 {
		set++
	}
	if len(archive.URL) > 0 {
		set++
	}
	if archive.OCI != nil {
		set++
	}
	if set > 1 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "Archive", archive.Type, "at most one of literal, url, or oci may be set"))
	}

	// Type==oci must carry an OCI payload: otherwise consumers that switch on
	// the type (e.g. `fission package getdeploy`, the executor's eligibility
	// read) would dereference a nil OCIArchive on a hand-authored or
	// corrupt Package.
	if archive.Type == ArchiveTypeOCI && archive.OCI == nil {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "Archive.Type", archive.Type, "type is 'oci' but no oci payload is set"))
	}

	if archive.OCI != nil {
		errs = errors.Join(errs, archive.OCI.Validate())
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
		if !r.IsEmpty() {
			errs = errors.Join(errs, r.Validate())
		}
	}

	// OCI delivery applies to deployment archives only: source archives feed
	// the builder, which has no OCI pull path.
	if spec.Source.OCI != nil {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "PackageSpec.Source", spec.Source.Type, "oci archives are supported on the deployment archive only"))
	}

	return errs
}

func (sts PackageStatus) Validate() error {
	var errs error

	switch sts.BuildStatus {
	// "" (empty) is the not-yet-processed state: with the Package /status
	// subresource, the apiserver strips the status set by the defaulting webhook
	// on create, so a package is admitted with an empty BuildStatus and the
	// buildermgr fills it in (setInitialBuildStatus). Reject only genuinely
	// unknown values.
	case "", BuildStatusPending, BuildStatusRunning, BuildStatusSucceeded, BuildStatusFailed, BuildStatusNone: // no op
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

	if spec.Streaming != nil {
		errs = errors.Join(errs, spec.Streaming.Validate())
	}

	if spec.Tool != nil {
		errs = errors.Join(errs, spec.Tool.Validate())
	}

	if spec.State != nil {
		errs = errors.Join(errs, spec.State.Validate())
		if spec.InvokeStrategy.ExecutionStrategy.ExecutorType == ExecutorTypeContainer {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidObject, "FunctionSpec.State", "", "the state API requires the poolmgr or newdeploy executor (the container executor has no fetcher sidecar to deliver a scoped token)"))
		}
	}

	if spec.Versioning != nil {
		errs = errors.Join(errs, spec.Versioning.Validate())
	}

	// Non-CEL admission check (pod-spec security). Kept in Validate() so the
	// CLI checks it client-side; the webhook runs it via ValidateForAdmission().
	// validateForAdmission also runs ProvisionedConcurrency.Validate(), so the
	// Target/Windows checks are covered without a duplicate call here.
	errs = errors.Join(errs, spec.validateForAdmission())

	// TODO Add below validation warning
	// if spec.FunctionTimeout <= 0 {
	// 	errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionTimeout value", spec.FunctionTimeout, "not a valid value. Should always be more than 0"))
	// }

	return errs
}

// validateForAdmission returns the FunctionSpec checks the API server cannot
// enforce via CEL and the admission webhook must still run: pod-spec security
// (iterating an embedded PodSpec exceeds the CEL cost budget; GHSA-v455-mv2v-5g92)
// and the RFC-0024 async invocation bounds (metav1.Duration CEL rules are unproven
// in this CRD, so the ordering/positivity checks live in Go and run at admission)
// and provisioned-concurrency Windows-emptiness (RFC-0026 PR 1 limitation).
func (spec FunctionSpec) validateForAdmission() error {
	var errs error
	errs = errors.Join(errs, ValidatePodSpecSafety("Function.spec.podspec", spec.PodSpec))
	if spec.Invocation != nil {
		errs = errors.Join(errs, spec.Invocation.Validate())
	}
	if spec.ProvisionedConcurrency != nil {
		errs = errors.Join(errs, spec.ProvisionedConcurrency.Validate())
	}
	return errs
}

const (
	// MaxAsyncAttempts bounds RetryPolicy.MaxAttempts. It matches the statestore
	// queue's fixed attempt budget (statestore.DefaultMaxAttempts): the async
	// dispatcher clamps to the same value, and a larger MaxAttempts would be
	// silently capped (the store dead-letters at its budget), so it is rejected at
	// admission instead. Raising it requires a per-message store budget (an
	// RFC-0024 follow-up).
	MaxAsyncAttempts = 3
	// MaxAsyncMaxAge bounds InvocationConfig.MaxAge — a platform ceiling so one
	// namespace cannot park work on the shared async queue indefinitely.
	MaxAsyncMaxAge = 7 * 24 * time.Hour
)

// Validate checks the async invocation config (only reached when
// FunctionSpec.Invocation is non-nil): an attempt budget in [1, MaxAsyncAttempts],
// a non-negative and well-ordered backoff schedule, and a max age in
// (0, MaxAsyncMaxAge]. These bounds keep the dispatcher's retry loop well-defined
// (a zero attempt budget or max age would mean "accepted but never deliverable")
// and keep one tenant from setting absurd values on the shared queue.
// validateBackoffBounds checks the ordering rules every RetryPolicy consumer
// shares (base >= 0, cap >= 0, cap >= base). Attempt budgets stay at the
// callers — async delivery clamps to MaxAsyncAttempts, workflows to
// MaxWorkflowAttempts — because they bound different things.
func (r *RetryPolicy) validateBackoffBounds(field string) error {
	var errs error
	if r.BackoffBase != nil && r.BackoffBase.Duration < 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".BackoffBase", r.BackoffBase.Duration, "must be >= 0"))
	}
	if r.BackoffCap != nil && r.BackoffCap.Duration < 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".BackoffCap", r.BackoffCap.Duration, "must be >= 0"))
	}
	if r.BackoffBase != nil && r.BackoffCap != nil && r.BackoffCap.Duration < r.BackoffBase.Duration {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".BackoffCap", r.BackoffCap.Duration, "must be >= BackoffBase"))
	}
	return errs
}

func (ic *InvocationConfig) Validate() error {
	var errs error
	r := ic.Retry
	if r.MaxAttempts != nil && *r.MaxAttempts < 1 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Invocation.Retry.MaxAttempts", *r.MaxAttempts, "must be >= 1"))
	}
	if r.MaxAttempts != nil && *r.MaxAttempts > MaxAsyncAttempts {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Invocation.Retry.MaxAttempts", *r.MaxAttempts, fmt.Sprintf("must be <= %d (the platform async attempt budget)", MaxAsyncAttempts)))
	}
	errs = errors.Join(errs, r.validateBackoffBounds("FunctionSpec.Invocation.Retry"))
	if ic.MaxAge != nil && ic.MaxAge.Duration <= 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Invocation.MaxAge", ic.MaxAge.Duration, "must be > 0"))
	}
	if ic.MaxAge != nil && ic.MaxAge.Duration > MaxAsyncMaxAge {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Invocation.MaxAge", ic.MaxAge.Duration, fmt.Sprintf("must be <= %s", MaxAsyncMaxAge)))
	}
	if ic.OnSuccess != nil {
		errs = errors.Join(errs, ic.OnSuccess.Validate("FunctionSpec.Invocation.OnSuccess"))
	}
	if ic.OnFailure != nil {
		errs = errors.Join(errs, ic.OnFailure.Validate("FunctionSpec.Invocation.OnFailure"))
	}
	return errs
}

// Validate checks a destination reference: exactly one of Function/Topic, a
// function destination that references a single named function (weights make no
// sense for a destination), and a topic destination on a supported provider —
// the built-in statestore (RFC-0027); broker types are rejected honestly until
// the egress phase lands rather than accepted and dropped.
func (d *DestinationRef) Validate(field string) error {
	switch {
	case d.Function == nil && d.Topic == nil:
		return MakeValidationErr(ErrorInvalidObject, field, "", "exactly one of function or topic must be set")
	case d.Function != nil && d.Topic != nil:
		return MakeValidationErr(ErrorInvalidObject, field, "", "only one of function or topic may be set")
	case d.Topic != nil:
		return d.Topic.Validate(field + ".topic")
	}
	if d.Function.Type != FunctionReferenceTypeFunctionName {
		return MakeValidationErr(ErrorInvalidValue, field+".function.type", d.Function.Type, fmt.Sprintf("must be %q", FunctionReferenceTypeFunctionName))
	}
	if strings.TrimSpace(d.Function.Name) == "" {
		return MakeValidationErr(ErrorInvalidValue, field+".function.name", d.Function.Name, "must not be empty")
	}
	return nil
}

// topicNameRegexp bounds statestore topic names to a stream-safe charset.
var topicNameRegexp = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// topicDestinationTypes are the MQ types a topic destination may target: the
// built-in statestore provider (direct EventLog publish) and the broker types
// whose classic heads run an RFC-0027 egress consumer. KEDA-only types (e.g.
// aws-sqs-queue) have no classic head and therefore no egress loop — rejected.
var topicDestinationTypes = map[MessageQueueType]struct{}{
	MessageQueueTypeStatestore: {},
	MessageQueueTypeKafka:      {},
}

// kafkaDestinationTopicRegexp mirrors the kafka provider's trigger-side topic
// grammar (alphanumeric first/last character), which is stricter than the
// statestore grammar: kafka rejects "." and ".." outright, and leading "_"
// names collide with broker-internal topics — admitting them would create
// destinations the broker refuses forever (retry churn straight to the DLQ).
var kafkaDestinationTopicRegexp = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*[a-zA-Z0-9]$`)

// Validate checks a topic destination: the MQ type must have a publish path
// (statestore direct, or a broker egress consumer), and the topic name must be
// safe in the namespaced stream mapping topic/<ns>/<topic>.
//
// Broker topics, like kafka MessageQueueTriggers, are CLUSTER-FLAT: a topic
// destination writes to the named broker topic as-is, with no per-namespace
// prefixing — isolation between tenants on the broker side is the broker's
// ACLs on the egress producer's principal. Whether a broker type's egress head
// is actually deployed is an install property admission cannot see; the router
// rejects publishes for undeployed types (EVENTING_EGRESS_TYPES).
func (tr *TopicRef) Validate(field string) error {
	if _, ok := topicDestinationTypes[tr.MessageQueueType]; !ok {
		return MakeValidationErr(ErrorInvalidValue, field+".messageQueueType", tr.MessageQueueType,
			fmt.Sprintf("topic destinations support %q (built-in) and %q (broker egress)", MessageQueueTypeStatestore, MessageQueueTypeKafka))
	}
	return ValidateTopicNameForMQType(field+".topic", string(tr.MessageQueueType), tr.Topic)
}

// ValidateTopicNameForMQType applies the base topic grammar plus the target
// type's own restrictions (kafka's stricter rule for kafka). Exported so every
// layer that handles a typed topic — admission, the mqpub publishers, the
// egress consumer sink, the topic admin API — applies the SAME rule and a name
// the broker refuses forever is rejected up front instead of churning retries
// into the DLQ.
func ValidateTopicNameForMQType(field, mqType, topic string) error {
	if err := ValidateTopicName(field, topic); err != nil {
		return err
	}
	if mqType == MessageQueueTypeKafka && !kafkaDestinationTopicRegexp.MatchString(topic) {
		return MakeValidationErr(ErrorInvalidValue, field, topic,
			"kafka topics must start and end with an alphanumeric character")
	}
	return nil
}

// ValidateTopicName bounds a statestore topic name: non-empty, at most 249
// characters (kafka parity), and [a-zA-Z0-9._-] only — critically excluding "/"
// so the topic/<ns>/<topic> stream mapping cannot alias across namespaces.
// Exported for the mqtrigger statestore provider's trigger validation.
func ValidateTopicName(field, topic string) error {
	if topic == "" || len(topic) > 249 || !topicNameRegexp.MatchString(topic) {
		return MakeValidationErr(ErrorInvalidValue, field, topic, "must be 1-249 characters of [a-zA-Z0-9._-]")
	}
	return nil
}

// Validate checks the streaming config: a known protocol, non-negative timeouts,
// and a max duration that is not below the idle timeout when both are set.
func (sc *StreamingConfig) Validate() error {
	var errs error

	switch sc.Protocol {
	case "", StreamingAuto, StreamingSSE, StreamingChunked, StreamingWebSocket:
		// ok
	default:
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Streaming.Protocol", sc.Protocol, "not a valid streaming protocol"))
	}

	if sc.IdleTimeoutSeconds < 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Streaming.IdleTimeoutSeconds", sc.IdleTimeoutSeconds, "must be >= 0"))
	}
	if sc.MaxDurationSeconds < 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Streaming.MaxDurationSeconds", sc.MaxDurationSeconds, "must be >= 0"))
	}
	if sc.IdleTimeoutSeconds > 0 && sc.MaxDurationSeconds > 0 && sc.MaxDurationSeconds < sc.IdleTimeoutSeconds {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Streaming.MaxDurationSeconds", sc.MaxDurationSeconds, "must be >= IdleTimeoutSeconds"))
	}

	return errs
}

// Validate checks the MCP tool config (only reached when FunctionSpec.Tool is
// non-nil, i.e. the function is advertised): a description is required, and a
// supplied InputSchema must parse as a JSON object carrying a "type" key (a
// cheap structural check — full JSON-Schema meta-validation is the agent's job,
// and CEL cannot parse arbitrary schemas). The ToolName pattern is enforced by
// the CRD kubebuilder marker.
func (tc *ToolConfig) Validate() error {
	var errs error

	if strings.TrimSpace(tc.Description) == "" {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Tool.Description", tc.Description, "a description is required when the function is exposed as an MCP tool"))
	}

	if tc.InputSchema != nil && len(tc.InputSchema.Raw) > 0 {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(tc.InputSchema.Raw, &obj); err != nil {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Tool.InputSchema", string(tc.InputSchema.Raw), "must be a JSON object"))
		} else if _, ok := obj["type"]; !ok {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Tool.InputSchema", string(tc.InputSchema.Raw), `must be a JSON Schema object with a "type" key`))
		}
	}

	return errs
}

// stateKeyspaceRegexp mirrors the StateConfig.Keyspace kubebuilder marker.
// The charset deliberately excludes ':' (token-derivation info-string
// separator) and '#' (the platform-reserved "<keyspace>#meta" quota sibling).
var stateKeyspaceRegexp = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// Validate checks the keyed-state config (only reached when FunctionSpec.State
// is non-nil): keyspace charset/length, non-negative quotas and TTL, and a
// well-formed sticky declaration. Re-checked in Go so the CLI validates
// client-side; the CRD markers enforce the same bounds at the API server.
func (sc *StateConfig) Validate() error {
	var errs error

	if sc.Keyspace != "" && (len(sc.Keyspace) > 63 || !stateKeyspaceRegexp.MatchString(sc.Keyspace)) {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.State.Keyspace", sc.Keyspace, "must be 1-63 characters matching ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$"))
	}
	if sc.MaxValueBytes < 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.State.MaxValueBytes", sc.MaxValueBytes, "must be >= 0"))
	}
	if sc.MaxKeys < 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.State.MaxKeys", sc.MaxKeys, "must be >= 0"))
	}
	if sc.DefaultTTL != nil && sc.DefaultTTL.Duration < 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.State.DefaultTTL", sc.DefaultTTL.Duration.String(), "must be >= 0"))
	}
	if sc.Sticky != nil {
		switch sc.Sticky.Source {
		case StickySourceHeader, StickySourceQueryParam:
			// ok
		default:
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.State.Sticky.Source", sc.Sticky.Source, "must be one of: header, queryparam"))
		}
		if sc.Sticky.Name == "" {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.State.Sticky.Name", sc.Sticky.Name, "a header or query-parameter name is required"))
		}
	}

	return errs
}

// Validate checks the provisioned concurrency config. In PR 1 only the base
// Target is meaningful — Windows is accepted in the CRD schema (for PR 2
// stability) but rejected here until the schedule parser is implemented.
// CEL already enforces Target >= 1 and the poolmgr-only constraint; this is
// defense-in-depth and the one check CEL cannot express (Windows empty).
func (pc *ProvisionedConcurrencyConfig) Validate() error {
	var errs error
	if pc.Target < 1 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.ProvisionedConcurrency.Target", pc.Target, "must be >= 1"))
	}

	if len(pc.Windows) > 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.ProvisionedConcurrency.Windows", len(pc.Windows),
			"scheduled windows are not yet supported (RFC-0026 PR 2)"))
	}

	return errs
}

// EffectiveKeyspace resolves the keyspace, defaulting to the function name.
func (sc *StateConfig) EffectiveKeyspace(fnName string) string {
	if sc.Keyspace != "" {
		return sc.Keyspace
	}
	return fnName
}

// EffectiveMaxValueBytes resolves the per-value size cap, applying the
// platform default when unset.
func (sc *StateConfig) EffectiveMaxValueBytes() int64 {
	if sc.MaxValueBytes > 0 {
		return sc.MaxValueBytes
	}
	return DefaultStateMaxValueBytes
}

// EffectiveMaxKeys resolves the live-key cap, applying the platform default
// when unset.
func (sc *StateConfig) EffectiveMaxKeys() int64 {
	if sc.MaxKeys > 0 {
		return sc.MaxKeys
	}
	return DefaultStateMaxKeys
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
	if spec.RouteConfig != nil {
		errs = errors.Join(errs, spec.RouteConfig.Validate())
	}
	if spec.CorsConfig != nil {
		errs = errors.Join(errs, spec.CorsConfig.Validate())
	}

	// Path validation. HTTPTrigger has no admission webhook on current main
	// (the API server's CEL evaluation is the admission gate); these checks
	// mirror the CEL rules on HTTPTriggerSpec so the CLI and the router
	// reconciler's status-Condition path agree with what the API server
	// admits. Closes GHSA-vchh-r53j-8mpw.
	prefix := ""
	if spec.Prefix != nil {
		prefix = *spec.Prefix
	}
	if spec.RelativeURL == "" && prefix == "" {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec", "",
			"at least one of relativeurl or prefix must be set"))
	}
	if spec.RelativeURL != "" {
		errs = errors.Join(errs, validateTriggerPath("HTTPTriggerSpec.RelativeURL", spec.RelativeURL))
	}
	if prefix != "" {
		errs = errors.Join(errs, validateTriggerPath("HTTPTriggerSpec.Prefix", prefix))
	}
	return errs
}

// routerReservedExactPaths are URL paths the router serves itself: liveness
// (/router-healthz), readiness (/readyz), version (/_version), and the
// chart-default auth login (/auth/login). Installations that change the auth
// path must still ensure their custom path does not collide with another
// HTTPTrigger; this list covers the defaults the router ships with.
var routerReservedExactPaths = map[string]struct{}{
	"/router-healthz": {},
	"/readyz":         {},
	"/_version":       {},
	"/auth/login":     {},
}

// routerInternalFunctionPrefix is the URL prefix the router serves on its
// internal listener (post-GHSA-3g33-6vg6-27m8) for direct function invocation.
// Triggers under this prefix would shadow internal routes if the public/
// internal listener split is misconfigured.
const routerInternalFunctionPrefix = "/fission-function/"

// validateTriggerPath enforces the URL-path safety invariants for RelativeURL
// and Prefix in HTTPTriggerSpec. Keep these checks aligned with the CEL rules
// on HTTPTriggerSpec in types.go.
func validateTriggerPath(field, path string) error {
	if !strings.HasPrefix(path, "/") {
		return MakeValidationErr(ErrorInvalidValue, field, path, "must start with '/'")
	}
	if path == "/" {
		return MakeValidationErr(ErrorInvalidValue, field, path, "root-only path '/' is not allowed")
	}
	// Reject any ".." path segment. Splitting on '/' (rather than substring
	// match) permits literal names like "..foo" or "foo..bar" while catching
	// the traversal form ".." that the router would otherwise resolve away.
	if slices.Contains(strings.Split(path, "/"), "..") {
		return MakeValidationErr(ErrorInvalidValue, field, path, "must not contain '..' path segments")
	}
	if _, reserved := routerReservedExactPaths[path]; reserved {
		return MakeValidationErr(ErrorInvalidValue, field, path, "collides with a router-owned path")
	}
	if strings.HasPrefix(path, routerInternalFunctionPrefix) {
		return MakeValidationErr(ErrorInvalidValue, field, path,
			"collides with the router-internal "+routerInternalFunctionPrefix+" prefix")
	}
	return nil
}

// Validate enforces the CORS spec invariants that browsers will reject
// at runtime, plus the URL-shape and duration-format invariants that
// surface as router-side configuration errors. Validation runs at
// admission so triggers never reconcile into a broken state.
func (c *HTTPTriggerCorsConfig) Validate() error {
	if c == nil {
		return nil
	}
	var errs error

	hasWildcard := slices.Contains(c.AllowOrigins, "*")
	if hasWildcard && c.AllowCredentials {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.CorsConfig",
			c.AllowOrigins, "AllowOrigins=[\"*\"] cannot be combined with AllowCredentials=true; browsers refuse the response"))
	}

	for _, origin := range c.AllowOrigins {
		if origin == "*" {
			continue
		}
		u, err := url.Parse(origin)
		if err != nil || u.Scheme == "" || u.Host == "" {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.CorsConfig.AllowOrigins",
				origin, "origin must be a valid URL with scheme and host (e.g. https://app.example.com)"))
			continue
		}
		// A CORS Origin header is scheme + host[:port] only — browsers
		// will never match an Access-Control-Allow-Origin that carries
		// a path, query, fragment, or userinfo, so accepting one here
		// would silently fail at runtime. Reject so the trigger fails
		// admission instead.
		if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.CorsConfig.AllowOrigins",
				origin, "origin must contain only scheme + host[:port]; path, query, fragment, and userinfo are not allowed"))
		}
	}

	if c.MaxAge != "" {
		d, err := time.ParseDuration(c.MaxAge)
		if err != nil {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.CorsConfig.MaxAge",
				c.MaxAge, fmt.Sprintf("must parse as time.Duration: %v", err)))
		} else if d < 0 {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.CorsConfig.MaxAge",
				c.MaxAge, "must be non-negative"))
		}
	}

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
		totalSize += int64(len(k)) + int64(len(v))
	}
	if totalSize > int64(totalAnnotationSizeLimitB) {
		msg := fmt.Sprintf("must have at most %v characters", totalSize)
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.IngressConfig.Annotations.value", totalAnnotationSizeLimitB, msg))
	}

	return errs
}

// Validate checks a RouteConfig. It mirrors the CEL rules on the RouteConfig
// type so the CLI and the admission gate agree on what is rejected. Cluster-side
// concerns the API server cannot know — whether the gateway provider is enabled,
// whether a default Gateway is configured — are not checked here; the router
// reconciler logs and retries those, so a misconfiguration surfaces in the
// router logs rather than as a CRD validation error.
func (config RouteConfig) Validate() error {
	var errs error

	switch config.Provider {
	case RouteProviderIngress, RouteProviderGateway:
	default:
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.RouteConfig.Provider", string(config.Provider), "must be one of: ingress, gateway"))
	}

	// Path is matched literally by both providers (the gateway provider emits a
	// PathPrefix match with this value), so it is validated as an absolute path,
	// not a regex.
	if len(config.Path) > 0 && !strings.HasPrefix(config.Path, "/") {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.RouteConfig.Path", config.Path, "must be an absolute path"))
	}

	for _, host := range config.Hostnames {
		if len(host) == 0 || host == "*" {
			continue
		}
		if strings.Contains(host, "*") {
			for _, msg := range validation.IsWildcardDNS1123Subdomain(host) {
				errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.RouteConfig.Hostnames", host, msg))
			}
			continue
		}
		for _, msg := range validation.IsDNS1123Subdomain(host) {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.RouteConfig.Hostnames", host, msg))
		}
	}

	var totalSize int64
	for k, v := range config.Annotations {
		for _, msg := range validation.IsQualifiedName(strings.ToLower(k)) {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.RouteConfig.Annotations.key", k, msg))
		}
		totalSize += int64(len(k)) + int64(len(v))
	}
	if totalSize > int64(totalAnnotationSizeLimitB) {
		msg := fmt.Sprintf("must have at most %v characters", totalSize)
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "HTTPTriggerSpec.RouteConfig.Annotations.value", totalAnnotationSizeLimitB, msg))
	}

	if config.Gateway != nil {
		for i, ref := range config.Gateway.ParentRefs {
			if len(ref.Name) == 0 {
				errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, fmt.Sprintf("HTTPTriggerSpec.RouteConfig.Gateway.ParentRefs[%d].Name", i), ref.Name, "must not be empty"))
			}
		}
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
		spec.FunctionReference.Validate(),
		spec.validateForAdmission())

	return errs
}

// validateForAdmission returns the KubernetesWatchTriggerSpec checks CEL cannot
// express: label-selector qualified key/value validation. (Type, namespace, and
// function-reference are enforced by CEL on the CRD.)
func (spec KubernetesWatchTriggerSpec) validateForAdmission() error {
	return ValidateKubeLabel("KubernetesWatchTriggerSpec.LabelSelector", spec.LabelSelector)
}

func (spec MessageQueueTriggerSpec) Validate() error {
	var errs error

	errs = errors.Join(errs, spec.FunctionReference.Validate())
	errs = errors.Join(errs, spec.validateForAdmission())

	return errs
}

// validateForAdmission returns the MessageQueueTriggerSpec checks CEL cannot
// express: message-queue type and topic/response-topic validity, looked up in
// the connector validator registry (pkg/mqtrigger/validator).
func (spec MessageQueueTriggerSpec) validateForAdmission() error {
	var errs error

	if !validator.IsValidMessageQueue(string(spec.MessageQueueType), spec.MqtKind) {
		errs = errors.Join(errs, MakeValidationErr(ErrorUnsupportedType, "MessageQueueTriggerSpec.MessageQueueType", spec.MessageQueueType, "not a supported message queue type"))
	} else {
		if !validator.IsValidTopic(string(spec.MessageQueueType), spec.Topic, spec.MqtKind) {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "MessageQueueTriggerSpec.Topic", spec.Topic, "not a valid topic"))
		}

		if len(spec.ResponseTopic) > 0 && !validator.IsValidTopic(string(spec.MessageQueueType), spec.ResponseTopic, spec.MqtKind) {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "MessageQueueTriggerSpec.ResponseTopic", spec.ResponseTopic, "not a valid topic"))
		}

		// An invalid ErrorTopic is worse than an invalid ResponseTopic: the
		// consumer refuses to advance past a poison event whose error-topic
		// publish keeps failing (E5), so the trigger wedges re-delivering it.
		if len(spec.ErrorTopic) > 0 && !validator.IsValidTopic((string)(spec.MessageQueueType), spec.ErrorTopic, spec.MqtKind) {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "MessageQueueTriggerSpec.ErrorTopic", spec.ErrorTopic, "not a valid topic"))
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

// ValidateForAdmission runs the Package checks the API server cannot enforce
// via CEL. There are none on the spec itself (archive/checksum/build-status are
// CEL-covered, reference names are CEL-covered); the webhook additionally
// enforces the archive literal-size limit and the cross-namespace environment
// check inline. Defined for a uniform webhook switch.
func (p *Package) ValidateForAdmission() error {
	return nil
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

// ValidateForAdmission runs only the checks the API server cannot enforce via
// CEL (pod-spec security). The admission webhook calls this instead of Validate()
// so it does not redundantly re-check the CEL-covered field rules.
func (f *Function) ValidateForAdmission() error {
	return f.Spec.validateForAdmission()
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
		e.Spec.Validate(),
		e.validateForAdmission())

	return errs
}

// validateForAdmission returns the Environment checks the API server cannot
// enforce via CEL and the admission webhook must still run: the runtime
// image/name invariant (a cross-field check comparing a container's image to
// spec.runtime.image and its name to the environment name) and pod-spec /
// container security on the runtime + builder podspecs and bare containers
// (GHSA-gx55-f84r-v3r7, GHSA-wmgg-3p4h-48x7, GHSA-m63v-2g9w-2w6v).
func (e *Environment) validateForAdmission() error {
	var errs error

	if e.Spec.Runtime.PodSpec != nil {
		for _, container := range e.Spec.Runtime.PodSpec.Containers {
			if container.Command == nil && container.Image == e.Spec.Runtime.Image && container.Name != e.Name {
				errs = errors.Join(errs, errors.New("container with image same as runtime image in podspec, must have name same as environment name"))
			}
		}
	}
	errs = errors.Join(errs, ValidatePodSpecSafety("Environment.spec.runtime.podspec", e.Spec.Runtime.PodSpec))
	errs = errors.Join(errs, ValidatePodSpecSafety("Environment.spec.builder.podspec", e.Spec.Builder.PodSpec))
	// The standalone Runtime.Container / Builder.Container fields are merged
	// into the runtime / builder pod without going through any PodSpec, so
	// ValidatePodSpecSafety above does not reach them; validate their
	// SecurityContext directly.
	errs = errors.Join(errs, ValidateContainerSafety("Environment.spec.runtime.container", e.Spec.Runtime.Container))
	errs = errors.Join(errs, ValidateContainerSafety("Environment.spec.builder.container", e.Spec.Builder.Container))
	return errs
}

// ValidateForAdmission runs only the checks the API server cannot enforce via
// CEL (see validateForAdmission). The admission webhook calls this instead of
// Validate() so it does not redundantly re-check the CEL-covered field rules.
func (e *Environment) ValidateForAdmission() error {
	return e.validateForAdmission()
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

// ValidateForAdmission runs only the checks the API server cannot enforce via
// CEL (label-selector qualified key/value). The admission webhook calls this
// instead of Validate(); its cross-namespace check stays inline in the webhook.
func (k *KubernetesWatchTrigger) ValidateForAdmission() error {
	return k.Spec.validateForAdmission()
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

// ValidateForAdmission runs only the checks the API server cannot enforce via
// CEL (message-queue type/topic validity). The admission webhook calls this
// instead of Validate(); its podspec allowlist stays inline in the webhook.
func (m *MessageQueueTrigger) ValidateForAdmission() error {
	return m.Spec.validateForAdmission()
}

func (ml *MessageQueueTriggerList) Validate() error {
	var errs error
	for _, m := range ml.Items {
		errs = errors.Join(errs, m.Validate())
	}
	return errs
}

//
// Function versions & aliases (RFC-0025)
//

// Validate checks a VersioningConfig: Mode must be one of the recognized
// values (empty defaults to auto at the reconciler), and Retain, when set,
// must be a positive GC floor — CEL already enforces this Minimum=1 marker at
// the API server, but the Go-side check keeps the CLI's client-side validation
// and Snapshot's nested re-check (see FunctionVersionSpec.Validate) honest.
func (vc *VersioningConfig) Validate() error {
	var errs error

	switch vc.Mode {
	case "", VersioningModeAuto, VersioningModeManual: // no op
	default:
		errs = errors.Join(errs, MakeValidationErr(ErrorUnsupportedType, "FunctionSpec.Versioning.Mode", vc.Mode, "not a valid versioning mode"))
	}

	if vc.Retain != nil && *vc.Retain < 1 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Versioning.Retain", *vc.Retain, "must be >= 1"))
	}

	return errs
}

// Validate checks a FunctionVersionSpec: the identity fields that pin this
// snapshot to a Function generation are required and well-formed, and the
// embedded Snapshot must itself validate (the same spec-level check
// Function.Validate runs) but must not carry its own Versioning config — a
// version is a versioning-config-free leaf, so a nested config would beg the
// question of what publishing that snapshot means and is rejected instead of
// silently ignored.
func (spec FunctionVersionSpec) Validate() error {
	var errs error

	errs = errors.Join(errs, ValidateKubeName("FunctionVersionSpec.FunctionName", spec.FunctionName))

	if len(spec.FunctionUID) == 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionVersionSpec.FunctionUID", spec.FunctionUID, "must not be empty"))
	}

	if spec.FunctionGeneration < 1 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionVersionSpec.FunctionGeneration", spec.FunctionGeneration, "must be >= 1"))
	}

	if spec.Sequence < 1 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionVersionSpec.Sequence", spec.Sequence, "must be >= 1"))
	}

	if len(spec.PackageDigest) == 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionVersionSpec.PackageDigest", spec.PackageDigest, "must not be empty"))
	}

	if spec.Snapshot.Versioning != nil {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidObject, "FunctionVersionSpec.Snapshot.Versioning", "", "snapshot must zero versioning to avoid recursion"))
	}

	errs = errors.Join(errs, spec.Snapshot.Validate())

	return errs
}

// aliasDigestRegexp matches the only digest form FunctionAliasSpec.PackageDigest
// accepts; it mirrors the field's kubebuilder Pattern marker in types.go (the
// same grammar as OCIArchive.Digest, kept as a separate var since the two
// fields validate independent objects).
var aliasDigestRegexp = ociDigestRegexp

// Validate checks a FunctionAliasSpec: FunctionName is a kube name, exactly
// one of Version (name-pinned) or PackageDigest (declarative, eventually
// resolved) targets the alias — mirroring the CRD's CEL XOR rule so the CLI
// validates client-side — and, when Weight is set for a split rollout,
// SecondaryVersion is required, is itself a kube name, and differs from
// Version (a split against itself is not a rollout).
func (spec FunctionAliasSpec) Validate() error {
	var errs error

	errs = errors.Join(errs, ValidateKubeName("FunctionAliasSpec.FunctionName", spec.FunctionName))

	hasVersion := spec.Version != ""
	hasDigest := spec.PackageDigest != ""
	switch {
	case !hasVersion && !hasDigest:
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidObject, "FunctionAliasSpec", "", "exactly one of version or packageDigest must be set"))
	case hasVersion && hasDigest:
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidObject, "FunctionAliasSpec", "", "only one of version or packageDigest may be set"))
	}

	if hasVersion {
		errs = errors.Join(errs, ValidateKubeName("FunctionAliasSpec.Version", spec.Version))
	}
	if hasDigest && !aliasDigestRegexp.MatchString(spec.PackageDigest) {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionAliasSpec.PackageDigest", spec.PackageDigest, "must be 'sha256:' followed by 64 hex characters"))
	}

	if spec.Weight != nil {
		if *spec.Weight < 0 || *spec.Weight > 100 {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionAliasSpec.Weight", *spec.Weight, "must be 0-100"))
		}
		if spec.SecondaryVersion == "" {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidObject, "FunctionAliasSpec.SecondaryVersion", "", "weight requires secondaryVersion"))
		}
	}

	if spec.SecondaryVersion != "" {
		errs = errors.Join(errs, ValidateKubeName("FunctionAliasSpec.SecondaryVersion", spec.SecondaryVersion))
		if spec.SecondaryVersion == spec.Version {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionAliasSpec.SecondaryVersion", spec.SecondaryVersion, "must differ from version"))
		}
	}

	return errs
}

// Validate checks a FunctionVersion: the object name must be exactly
// "<functionName>-v<sequence>" (the version-control loop and `fission fn
// publish` both mint names this way; a mismatch here means the object was
// hand-authored or corrupted, and the name-derived lookup every consumer
// relies on — GC, alias resolution — would silently miss it), plus the
// embedded spec.
func (fv *FunctionVersion) Validate() error {
	var errs error

	wantName := fmt.Sprintf("%s-v%d", fv.Spec.FunctionName, fv.Spec.Sequence)
	if fv.Name != wantName {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionVersion.ObjectMeta.Name", fv.Name, fmt.Sprintf("must be %q (<functionName>-v<sequence>)", wantName)))
	}
	errs = errors.Join(errs, validateNS("FunctionVersion.ObjectMeta.Namespace", fv.Namespace))

	errs = errors.Join(errs, fv.Spec.Validate())

	return errs
}

func (fa *FunctionAlias) Validate() error {
	var errs error

	errs = errors.Join(errs,
		validateMetadata("FunctionAlias", fa.ObjectMeta),
		fa.Spec.Validate())

	return errs
}
