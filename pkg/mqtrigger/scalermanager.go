// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqtrigger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/go-logr/logr"
	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	kedaClient "github.com/kedacore/keda/v2/pkg/generated/clientset/versioned"
	"golang.org/x/sync/errgroup"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/util"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/crmanager"
)

var (
	matchFirstCap = regexp.MustCompile("(.)([A-Z][a-z]+)")
	matchAllCap   = regexp.MustCompile("([a-z0-9])([A-Z])")
)

// scalerReconcileConcurrency lets independent keda-kind MessageQueueTriggers
// reconcile their KEDA objects in parallel, matching the per-trigger goroutine
// fan-out the old AddFunc/UpdateFunc handler used.
const scalerReconcileConcurrency = 4

// StartScalerManager watches for changes in MessageQueueTrigger and,
// Based on changes, it Creates, Updates and Deletes Objects of Kind ScaledObjects, AuthenticationTriggers and Deployments.
//
// routerURL is propagated as the HTTP_ENDPOINT env var on KEDA-managed
// connector deployments. The fission-bundle entrypoint resolves
// ROUTER_INTERNAL_URL from the environment before invoking this
// function, so KEDA connectors point at the router's internal
// listener (where /fission-function/... lives after
// GHSA-3g33-6vg6-27m8). KEDA connector images sourced from upstream do
// not currently sign their requests, so deployments that enable both
// KEDA scalers and the HMAC verifier on the router need connector
// images that have been updated to sign — operators should either
// keep FISSION_INTERNAL_AUTH_SECRET unset (rely on NetworkPolicy for
// isolation, which still gates port 8889 to fission-bundle pods only)
// or build signing-aware connector images. This is a documented
// rollout caveat for advisory 4.
func StartScalerManager(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, _ *errgroup.Group, routerURL string) error {
	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("failed to get fission client: %w", err)
	}
	kubeClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to get kubernetes client: %w", err)
	}
	kedaClient, err := clientGen.GetKedaClient()
	if err != nil {
		return fmt.Errorf("failed to get keda client: %w", err)
	}
	restConfig, err := clientGen.GetRestConfig()
	if err != nil {
		return fmt.Errorf("failed to get rest config: %w", err)
	}

	err = crd.WaitForFunctionCRDs(ctx, logger, fissionClient)
	if err != nil {
		return fmt.Errorf("error waiting for CRDs: %w", err)
	}

	// Active-passive HA via native controller-runtime leader election: only the
	// elected leader reconciles KEDA ScaledObjects, so two replicas don't race
	// on the same objects. No-op when LEADER_ELECTION_ENABLED is unset
	// (single-replica default). The reconciler watches MessageQueueTriggers
	// through the Manager's namespace-scoped cache (FissionCacheOptions),
	// reproducing the per-namespace informers this subsystem used before and
	// keeping RBAC unchanged. A distinct lock name keeps this leader election
	// independent of the --mqt subsystem's ("fission-mqtrigger").
	crMgr, err := crmanager.NewLeaderElected(restConfig, "fission-mqt-keda-scaler", logger)
	if err != nil {
		return err
	}

	r := newScalerReconciler(logger, crMgr.GetClient(), kedaClient, kubeClient, routerURL)
	if err := controller.RegisterTenantScopedWithConcurrency(crMgr, &fv1.MessageQueueTrigger{}, r, "mqt-keda-scaler", scalerReconcileConcurrency); err != nil {
		return fmt.Errorf("error registering mqt keda scaler reconciler: %w", err)
	}
	return crMgr.Start(ctx)
}

func toEnvVar(str string) string {
	envVar := matchFirstCap.ReplaceAllString(str, "${1}_${2}")
	envVar = matchAllCap.ReplaceAllString(envVar, "${1}_${2}")
	return strings.ToUpper(envVar)
}

func getEnvVarlist(ctx context.Context, mqt *fv1.MessageQueueTrigger, routerURL string, kubeClient kubernetes.Interface) ([]apiv1.EnvVar, error) {
	// RFC-0025: append the alias/version suffix when the reference carries
	// one; resolution stays entirely router-side.
	url := routerURL + "/" + strings.TrimPrefix(utils.UrlForFunctionReference(mqt.Spec.FunctionReference, mqt.Namespace), "/")
	envVars := []apiv1.EnvVar{
		{
			Name:  "TOPIC",
			Value: mqt.Spec.Topic,
		},
		{
			Name:  "HTTP_ENDPOINT",
			Value: url,
		},
		{
			Name:  "ERROR_TOPIC",
			Value: mqt.Spec.ErrorTopic,
		},
		{
			Name:  "RESPONSE_TOPIC",
			Value: mqt.Spec.ResponseTopic,
		},
		{
			Name:  "SOURCE_NAME",
			Value: mqt.Name,
		},
		{
			Name:  "MAX_RETRIES",
			Value: strconv.Itoa(mqt.Spec.MaxRetries),
		},
		{
			Name:  "CONTENT_TYPE",
			Value: mqt.Spec.ContentType,
		},
	}
	// Metadata Fields
	for key, value := range mqt.Spec.Metadata {
		envVars = append(envVars, apiv1.EnvVar{
			Name:  toEnvVar(key),
			Value: value,
		})
	}

	// Add Auth Fields. SECURITY: emit secret-derived env vars as
	// SecretKeyRef rather than materializing literal values into the
	// Deployment spec. The connector pod resolves the values at start
	// time via its own ServiceAccount, restoring the Kubernetes-RBAC
	// boundary on secret reads. The controller still calls Secrets().Get
	// to enumerate the keys it must surface as env vars (so the response
	// body, including secret values, briefly transits controller memory),
	// but the values are NOT written into the Deployment object and are
	// NOT logged. See GHSA-7m8x-qg2j-4m3v.
	secretName := mqt.Spec.Secret
	if len(secretName) > 0 {
		secret, err := kubeClient.CoreV1().Secrets(mqt.Namespace).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		for key := range secret.Data {
			envVars = append(envVars, apiv1.EnvVar{
				Name: toEnvVar(key),
				ValueFrom: &apiv1.EnvVarSource{
					SecretKeyRef: &apiv1.SecretKeySelector{
						LocalObjectReference: apiv1.LocalObjectReference{Name: secretName},
						Key:                  key,
					},
				},
			})
		}
	}
	return envVars, nil
}

func checkAndUpdateTriggerFields(mqt, newMqt *fv1.MessageQueueTrigger) bool {
	updated := false
	if len(newMqt.Spec.Topic) > 0 && newMqt.Spec.Topic != mqt.Spec.Topic {
		mqt.Spec.Topic = newMqt.Spec.Topic
		updated = true
	}
	if len(newMqt.Spec.ResponseTopic) > 0 && newMqt.Spec.ResponseTopic != mqt.Spec.ResponseTopic {
		mqt.Spec.ResponseTopic = newMqt.Spec.ResponseTopic
		updated = true
	}
	if len(newMqt.Spec.ErrorTopic) > 0 && newMqt.Spec.ErrorTopic != mqt.Spec.ErrorTopic {
		mqt.Spec.ErrorTopic = newMqt.Spec.ErrorTopic
		updated = true
	}
	if newMqt.Spec.MaxRetries >= 0 && newMqt.Spec.MaxRetries != mqt.Spec.MaxRetries {
		mqt.Spec.MaxRetries = newMqt.Spec.MaxRetries
		updated = true
	}
	if len(newMqt.Spec.FunctionReference.Name) > 0 && newMqt.Spec.FunctionReference.Name != mqt.Spec.FunctionReference.Name {
		mqt.Spec.FunctionReference.Name = newMqt.Spec.FunctionReference.Name
		updated = true
	}
	if len(newMqt.Spec.ContentType) > 0 && newMqt.Spec.ContentType != mqt.Spec.ContentType {
		mqt.Spec.ContentType = newMqt.Spec.ContentType
		updated = true
	}
	if *newMqt.Spec.PollingInterval >= 0 && *newMqt.Spec.PollingInterval != *mqt.Spec.PollingInterval {
		mqt.Spec.PollingInterval = newMqt.Spec.PollingInterval
		updated = true
	}
	if *newMqt.Spec.CooldownPeriod >= 0 && *newMqt.Spec.CooldownPeriod != *mqt.Spec.CooldownPeriod {
		mqt.Spec.CooldownPeriod = newMqt.Spec.CooldownPeriod
		updated = true
	}
	if *newMqt.Spec.MinReplicaCount >= 0 && *newMqt.Spec.MinReplicaCount != *mqt.Spec.MinReplicaCount {
		mqt.Spec.MinReplicaCount = newMqt.Spec.MinReplicaCount
		updated = true
	}
	if *newMqt.Spec.MaxReplicaCount >= 0 && *newMqt.Spec.MaxReplicaCount != *mqt.Spec.MaxReplicaCount {
		mqt.Spec.MaxReplicaCount = newMqt.Spec.MaxReplicaCount
		updated = true
	}
	if len(newMqt.Spec.FunctionReference.Name) > 0 && newMqt.Spec.FunctionReference.Name != mqt.Spec.FunctionReference.Name {
		mqt.Spec.FunctionReference.Name = newMqt.Spec.FunctionReference.Name
		updated = true
	}

	if !reflect.DeepEqual(newMqt.Spec.PodSpec, mqt.Spec.PodSpec) {
		mqt.Spec.PodSpec = newMqt.Spec.PodSpec
		updated = true
	}

	for key, value := range newMqt.Spec.Metadata {
		if val, ok := mqt.Spec.Metadata[key]; ok && val != value {
			mqt.Spec.Metadata[key] = value
			updated = true
		}
	}

	if len(newMqt.Spec.Secret) > 0 && newMqt.Spec.Secret != mqt.Spec.Secret {
		mqt.Spec.Secret = newMqt.Spec.Secret
		updated = true
	}

	if newMqt.Spec.MqtKind != mqt.Spec.MqtKind {
		mqt.Spec.MqtKind = newMqt.Spec.MqtKind
		updated = true
	}

	return updated
}

func createKedaObjects(ctx context.Context, logger logr.Logger, kedaClient kedaClient.Interface, kubeClient kubernetes.Interface, mqt *fv1.MessageQueueTrigger, routerURL string) error {
	// An AlreadyExists Create means the object is already present, which is the
	// desired end state of the create path. Tolerating it keeps the create
	// idempotent: after a scaler-manager restart the reconciler's last-seen cache
	// is empty, so every existing keda-kind MQT re-enters this path (old == nil) —
	// exactly as the old informer's AddFunc re-fired on resync. The handler merely
	// logged the AlreadyExists; here we must also avoid returning it, or the
	// reconciler would requeue the same Create forever.
	created := func(err error) bool { return err == nil || apierrors.IsAlreadyExists(err) }

	// Roll back only what THIS call newly created (err == nil), never resources
	// that already existed (AlreadyExists). Deleting a pre-existing Deployment on a
	// later transient failure would tear down a live connector and cause downtime.
	authNewlyCreated := false
	deployNewlyCreated := false
	rollback := func() {
		if deployNewlyCreated {
			if derr := deleteDeployment(ctx, mqt.Name, mqt.Namespace, kubeClient); derr != nil {
				logger.Error(derr, "Failed to delete Deployment")
			}
		}
		if authNewlyCreated {
			if derr := deleteAuthTrigger(ctx, kedaClient, authTriggerName(mqt.Name), mqt.Namespace); derr != nil {
				logger.Error(derr, "Failed to delete Authentication Trigger")
			}
		}
	}

	authenticationRef := ""
	if len(mqt.Spec.Secret) > 0 {
		authenticationRef = authTriggerName(mqt.Name)
		err := createAuthTrigger(ctx, kedaClient, mqt, authenticationRef, kubeClient)
		if !created(err) {
			logger.Error(err, "Failed to create Authentication Trigger")
			return err
		}
		authNewlyCreated = err == nil
	}

	deployErr := createDeployment(ctx, logger, mqt, routerURL, kubeClient)
	if !created(deployErr) {
		logger.Error(deployErr, "Failed to create Deployment")
		rollback()
		return deployErr
	}
	deployNewlyCreated = deployErr == nil

	if err := createScaledObject(ctx, kedaClient, mqt, authenticationRef); !created(err) {
		logger.Error(err, "Failed to create ScaledObject")
		rollback()
		return err
	}
	return nil
}

func cleanupKedaObjects(ctx context.Context, logger logr.Logger, kedaClient kedaClient.Interface, kubeClient kubernetes.Interface, mqt *fv1.MessageQueueTrigger) error {
	var errs error
	// A NotFound delete means the object is already gone, which is the desired end
	// state of cleanup. Tolerating it keeps teardown idempotent: when the
	// reconciler retries a keda->fission transition (or a deployment/scaledobject
	// was already GC'd), a NotFound must not bubble up as an error and requeue
	// forever. NotFound is also not logged — it is the expected outcome on a retry,
	// so error-level logging it would be pure noise; only unexpected errors are
	// logged and joined.
	collect := func(msg string, err error) {
		if err == nil || apierrors.IsNotFound(err) {
			return
		}
		logger.Error(err, msg)
		errs = errors.Join(errs, err)
	}

	if len(mqt.Spec.Secret) > 0 {
		collect("Failed to delete Authentication Trigger", deleteAuthTrigger(ctx, kedaClient, authTriggerName(mqt.Name), mqt.Namespace))
	}
	collect("Failed to delete Deployment", deleteDeployment(ctx, mqt.Name, mqt.Namespace, kubeClient))
	collect("Failed to delete ScaledObject", deleteScaledObject(ctx, kedaClient, mqt.Name, mqt.Namespace))
	return errs
}

func getAuthTriggerSpec(ctx context.Context, mqt *fv1.MessageQueueTrigger, authenticationRef string, kubeClient kubernetes.Interface) (*kedav1alpha1.TriggerAuthentication, error) {
	secret, err := kubeClient.CoreV1().Secrets(mqt.Namespace).Get(ctx, mqt.Spec.Secret, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	var secretTargetRefFields []kedav1alpha1.AuthSecretTargetRef
	for secretField := range secret.Data {
		secretTargetRefFields = append(secretTargetRefFields, kedav1alpha1.AuthSecretTargetRef{
			Name:      mqt.Spec.Secret,
			Parameter: secretField,
			Key:       secretField,
		})
	}

	authTriggerObj := &kedav1alpha1.TriggerAuthentication{
		ObjectMeta: metav1.ObjectMeta{
			Name:            authenticationRef,
			Namespace:       mqt.Namespace,
			OwnerReferences: []metav1.OwnerReference{newOwnerReference(mqt.Name, mqt.UID)},
		},
		Spec: kedav1alpha1.TriggerAuthenticationSpec{
			SecretTargetRef: secretTargetRefFields,
		},
	}
	return authTriggerObj, nil
}

func createAuthTrigger(ctx context.Context, client kedaClient.Interface, mqt *fv1.MessageQueueTrigger, authenticationRef string, kubeClient kubernetes.Interface) error {
	authTriggerObj, err := getAuthTriggerSpec(ctx, mqt, authenticationRef, kubeClient)
	if err != nil {
		return err
	}

	_, err = client.KedaV1alpha1().TriggerAuthentications(authTriggerObj.Namespace).Create(ctx, authTriggerObj, metav1.CreateOptions{})
	return err
}

func updateAuthTrigger(ctx context.Context, client kedaClient.Interface, mqt *fv1.MessageQueueTrigger, authenticationRef string, kubeClient kubernetes.Interface) error {
	oldAuthTriggerObj, err := client.KedaV1alpha1().TriggerAuthentications(mqt.Namespace).Get(ctx, authenticationRef, metav1.GetOptions{})
	if err != nil {
		return err
	}
	resourceVersion := oldAuthTriggerObj.GetResourceVersion()

	authTriggerObj, err := getAuthTriggerSpec(ctx, mqt, authenticationRef, kubeClient)
	if err != nil {
		return err
	}
	authTriggerObj.SetResourceVersion(resourceVersion)
	_, err = client.KedaV1alpha1().TriggerAuthentications(authTriggerObj.Namespace).Update(ctx, authTriggerObj, metav1.UpdateOptions{})
	return err
}

func deleteAuthTrigger(ctx context.Context, client kedaClient.Interface, name, namespace string) error {
	err := client.KedaV1alpha1().TriggerAuthentications(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	return err
}

func getDeploymentSpec(ctx context.Context, logger logr.Logger, mqt *fv1.MessageQueueTrigger, routerURL string, kubeClient kubernetes.Interface) (*appsv1.Deployment, error) {
	envVars, err := getEnvVarlist(ctx, mqt, routerURL, kubeClient)
	if err != nil {
		return nil, err
	}
	imageName := fmt.Sprintf("%s_image", string(mqt.Spec.MessageQueueType))
	image := os.Getenv(strings.ToUpper(imageName))
	imagePullPolicy := utils.GetImagePullPolicy(os.Getenv("CONNECTOR_IMAGE_PULL_POLICY"))

	podSpec := &apiv1.PodSpec{
		Containers: []apiv1.Container{
			{
				Name:            mqt.Name,
				Image:           image,
				ImagePullPolicy: imagePullPolicy,
				Env:             envVars,
			},
		},
	}
	// SECURITY: only an allowlist of PodSpec fields from the user-supplied
	// mqt.Spec.PodSpec is honoured. Image/Command/Args/Env/Volumes/etc. are
	// dropped here; the validating webhook rejects them at admission time
	// so a normally-configured cluster never reaches this branch with bad
	// input. See GHSA-7m8x-qg2j-4m3v.
	mergedSpec, dropped := util.MergeAllowedPodSpecFields(podSpec, mqt.Spec.PodSpec)
	if len(dropped) > 0 {
		logger.Info("dropped disallowed PodSpec fields from MessageQueueTrigger",
			"trigger", mqt.Name, "namespace", mqt.Namespace, "dropped", dropped)
	}
	podSpec = mergedSpec

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: mqt.Name,
			Labels: map[string]string{
				"app": mqt.Name,
			},
			OwnerReferences: []metav1.OwnerReference{newOwnerReference(mqt.Name, mqt.UID)},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": mqt.Name,
				},
			},
			Template: apiv1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": mqt.Name,
					},
				},
				Spec: *podSpec,
			},
		},
	}, nil
}

func createDeployment(ctx context.Context, logger logr.Logger, mqt *fv1.MessageQueueTrigger, routerURL string, kubeClient kubernetes.Interface) error {
	deployment, err := getDeploymentSpec(ctx, logger, mqt, routerURL, kubeClient)
	if err != nil {
		return err
	}
	_, err = kubeClient.AppsV1().Deployments(mqt.ObjectMeta.Namespace).Create(ctx, deployment, metav1.CreateOptions{})
	return err
}

func updateDeployment(ctx context.Context, logger logr.Logger, mqt *fv1.MessageQueueTrigger, routerURL string, kubeClient kubernetes.Interface) error {
	deployment, err := getDeploymentSpec(ctx, logger, mqt, routerURL, kubeClient)
	if err != nil {
		return err
	}
	_, err = kubeClient.AppsV1().Deployments(mqt.ObjectMeta.Namespace).Update(ctx, deployment, metav1.UpdateOptions{})
	return err
}

func deleteDeployment(ctx context.Context, name string, namespace string, kubeClient kubernetes.Interface) error {
	deletePolicy := metav1.DeletePropagationForeground
	return kubeClient.AppsV1().Deployments(namespace).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	})
}

func getScaledObject(mqt *fv1.MessageQueueTrigger, authenticationRef string) *kedav1alpha1.ScaledObject {
	trigger := kedav1alpha1.ScaleTriggers{
		Type:     string(mqt.Spec.MessageQueueType),
		Metadata: mqt.Spec.Metadata,
	}
	// Only attach an AuthenticationRef for secret-bearing triggers. An
	// empty-named ref (authenticationRef == "") is meaningless to KEDA and would
	// dangle a reference to a nonexistent TriggerAuthentication.
	if authenticationRef != "" {
		trigger.AuthenticationRef = &kedav1alpha1.AuthenticationRef{Name: authenticationRef}
	}
	return &kedav1alpha1.ScaledObject{
		ObjectMeta: metav1.ObjectMeta{
			Name:            mqt.Name,
			Namespace:       mqt.Namespace,
			OwnerReferences: []metav1.OwnerReference{newOwnerReference(mqt.Name, mqt.UID)},
		},

		Spec: kedav1alpha1.ScaledObjectSpec{
			CooldownPeriod:  mqt.Spec.CooldownPeriod,
			MaxReplicaCount: mqt.Spec.MaxReplicaCount,
			MinReplicaCount: mqt.Spec.MinReplicaCount,
			PollingInterval: mqt.Spec.PollingInterval,
			ScaleTargetRef: &kedav1alpha1.ScaleTarget{
				Name: mqt.Name,
			},
			Triggers: []kedav1alpha1.ScaleTriggers{trigger},
		},
	}
}

func createScaledObject(ctx context.Context, client kedaClient.Interface, mqt *fv1.MessageQueueTrigger, authenticationRef string) error {
	scaledObject := getScaledObject(mqt, authenticationRef)
	_, err := client.KedaV1alpha1().ScaledObjects(scaledObject.Namespace).Create(ctx, scaledObject, metav1.CreateOptions{})
	return err
}

func updateScaledObject(ctx context.Context, client kedaClient.Interface, mqt *fv1.MessageQueueTrigger, authenticationRef string) error {
	oldScaledObject, err := client.KedaV1alpha1().ScaledObjects(mqt.ObjectMeta.Namespace).Get(ctx, mqt.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	resourceVersion := oldScaledObject.GetResourceVersion()

	scaledObject := getScaledObject(mqt, authenticationRef)
	scaledObject.SetResourceVersion(resourceVersion)

	_, err = client.KedaV1alpha1().ScaledObjects(mqt.ObjectMeta.Namespace).Update(ctx, scaledObject, metav1.UpdateOptions{})
	return err
}

func deleteScaledObject(ctx context.Context, client kedaClient.Interface, name, namespace string) error {
	return client.KedaV1alpha1().ScaledObjects(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}
