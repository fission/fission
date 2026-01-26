package mqtrigger

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"

	"github.com/go-logr/logr"
	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	kedaClient "github.com/kedacore/keda/v2/pkg/generated/clientset/versioned"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/util"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/manager"
)

var (
	matchFirstCap = regexp.MustCompile("(.)([A-Z][a-z]+)")
	matchAllCap   = regexp.MustCompile("([a-z0-9])([A-Z])")
)

func mqTriggerEventHandlers(ctx context.Context, logger logr.Logger, kubeClient kubernetes.Interface, kedaClient kedaClient.Interface, routerURL string) k8sCache.ResourceEventHandlerFuncs {
	return k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			go func() {
				mqt := obj.(*fv1.MessageQueueTrigger)
				if mqt.Spec.MqtKind == MqtKindFission {
					return
				}
				logger.V(1).Info("Create deployment for Scaler Object", "mqt", mqt.ObjectMeta, "mqt.Spec", mqt.Spec)
				createKedaObjects(ctx, logger, kedaClient, kubeClient, mqt, routerURL)
			}()
		},
		UpdateFunc: func(obj any, newObj any) {
			go func() {
				mqt := obj.(*fv1.MessageQueueTrigger)
				newMqt := newObj.(*fv1.MessageQueueTrigger)
				mqtkindKedaToFission := (mqt.Spec.MqtKind == MqtKindKeda && newMqt.Spec.MqtKind == MqtKindFission)
				mqtkindFissionToKeda := (mqt.Spec.MqtKind == MqtKindFission && newMqt.Spec.MqtKind == MqtKindKeda)
				// If mqtkind is updated to fission from keda then
				// delete keda objects previously created for mqtkind keda.
				if mqtkindKedaToFission {
					logger.V(1).Info("Mqtkind updated to fission from keda, cleanup keda objects", "mqt", newMqt.ObjectMeta, "mqt.Spec", newMqt.Spec)
					cleanupKedaObjects(ctx, logger, kedaClient, kubeClient, mqt)
					return
				}
				// If mqtkind is updated to keda from fission then
				// create keda objects
				if mqtkindFissionToKeda {
					logger.V(1).Info("Mqtkind changed to keda from fission, create keda objects", "mqt", newMqt.ObjectMeta, "mqt.Spec", newMqt.Spec)
					createKedaObjects(ctx, logger, kedaClient, kubeClient, newMqt, routerURL)
					return
				}

				updated := checkAndUpdateTriggerFields(mqt, newMqt)
				if mqt.Spec.MqtKind == MqtKindFission {
					return
				}
				if !updated {
					logger.Info("Trigger unchanged, no changes found in trigger fields", "trigger_name", mqt.Name)
					return
				}

				authenticationRef := ""
				if len(newMqt.Spec.Secret) > 0 && newMqt.Spec.Secret != mqt.Spec.Secret {
					authenticationRef = authTriggerName(mqt.Name)
					if err := updateAuthTrigger(ctx, kedaClient, mqt, authenticationRef, kubeClient); err != nil {
						logger.Error(err, "Failed to update Authentication Trigger")
						return
					}
				}

				if err := updateDeployment(ctx, mqt, routerURL, kubeClient); err != nil {
					logger.Error(err, "Failed to Update Deployment")
					return
				}

				if err := updateScaledObject(ctx, kedaClient, mqt, authenticationRef); err != nil {
					logger.Error(err, "Failed to Update ScaledObject")
					return
				}
			}()
		},
	}

}

// StartScalerManager watches for changes in MessageQueueTrigger and,
// Based on changes, it Creates, Updates and Deletes Objects of Kind ScaledObjects, AuthenticationTriggers and Deployments
func StartScalerManager(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr manager.Interface, routerURL string) error {
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

	err = crd.WaitForFunctionCRDs(ctx, logger, fissionClient)
	if err != nil {
		return fmt.Errorf("error waiting for CRDs: %w", err)
	}

	for _, informer := range utils.GetInformersForNamespaces(fissionClient, time.Minute*30, fv1.MessageQueueResource) {
		_, err := informer.AddEventHandler(mqTriggerEventHandlers(ctx, logger, kubeClient, kedaClient, routerURL))
		if err != nil {
			return err
		}
		mgr.Add(ctx, func(ctx context.Context) {
			informer.Run(ctx.Done())
		})
		if ok := k8sCache.WaitForCacheSync(ctx.Done(), informer.HasSynced); !ok {
			logger.Info("failed to wait for caches to sync")
			os.Exit(1)
		}
	}
	return nil
}

func toEnvVar(str string) string {
	envVar := matchFirstCap.ReplaceAllString(str, "${1}_${2}")
	envVar = matchAllCap.ReplaceAllString(envVar, "${1}_${2}")
	return strings.ToUpper(envVar)
}

func getEnvVarlist(ctx context.Context, mqt *fv1.MessageQueueTrigger, routerURL string, kubeClient kubernetes.Interface) ([]apiv1.EnvVar, error) {
	url := routerURL + "/" + strings.TrimPrefix(utils.UrlForFunction(mqt.Spec.FunctionReference.Name, mqt.Namespace), "/")
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

	// Add Auth Fields
	secretName := mqt.Spec.Secret
	if len(secretName) > 0 {
		secret, err := kubeClient.CoreV1().Secrets(mqt.Namespace).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		for key, value := range secret.Data {
			envVars = append(envVars, apiv1.EnvVar{
				Name:  toEnvVar(key),
				Value: string(value),
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

func createKedaObjects(ctx context.Context, logger logr.Logger, kedaClient kedaClient.Interface, kubeClient kubernetes.Interface, mqt *fv1.MessageQueueTrigger, routerURL string) {
	authenticationRef := ""
	if len(mqt.Spec.Secret) > 0 {
		authenticationRef = authTriggerName(mqt.Name)
		err := createAuthTrigger(ctx, kedaClient, mqt, authenticationRef, kubeClient)
		if err != nil {
			logger.Error(err, "Failed to create Authentication Trigger")
			return
		}
	}

	if err := createDeployment(ctx, mqt, routerURL, kubeClient); err != nil {
		logger.Error(err, "Failed to create Deployment")
		if len(authenticationRef) > 0 {
			err = deleteAuthTrigger(ctx, kedaClient, authenticationRef, mqt.Namespace)
			if err != nil {
				logger.Error(err, "Failed to delete Authentication Trigger")
			}
		}
		return
	}

	if err := createScaledObject(ctx, kedaClient, mqt, authenticationRef); err != nil {
		logger.Error(err, "Failed to create ScaledObject")
		if len(authenticationRef) > 0 {
			if err = deleteAuthTrigger(ctx, kedaClient, authenticationRef, mqt.Namespace); err != nil {
				logger.Error(err, "Failed to delete Authentication Trigger")
			}
		}
		if err = deleteDeployment(ctx, mqt.Name, mqt.Namespace, kubeClient); err != nil {
			logger.Error(err, "Failed to delete Deployment")
		}
	}
}

func cleanupKedaObjects(ctx context.Context, logger logr.Logger, kedaClient kedaClient.Interface, kubeClient kubernetes.Interface, mqt *fv1.MessageQueueTrigger) {
	authenticationRef := ""
	if len(mqt.Spec.Secret) > 0 {
		authenticationRef = authTriggerName(mqt.Name)
	}

	if len(authenticationRef) > 0 {
		err := deleteAuthTrigger(ctx, kedaClient, authenticationRef, mqt.Namespace)
		if err != nil {
			logger.Error(err, "Failed to delete Authentication Trigger")
		}
	}

	if err := deleteDeployment(ctx, mqt.Name, mqt.Namespace, kubeClient); err != nil {
		logger.Error(err, "Failed to delete Deployment")
	}

	if err := deleteScaledObject(ctx, kedaClient, mqt.Name, mqt.Namespace); err != nil {
		logger.Error(err, "Failed to delete ScaledObject")
	}
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

func getDeploymentSpec(ctx context.Context, mqt *fv1.MessageQueueTrigger, routerURL string, kubeClient kubernetes.Interface) (*appsv1.Deployment, error) {
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
	podSpec, err = util.MergePodSpec(podSpec, mqt.Spec.PodSpec)
	if err != nil {
		return nil, err
	}

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

func createDeployment(ctx context.Context, mqt *fv1.MessageQueueTrigger, routerURL string, kubeClient kubernetes.Interface) error {
	deployment, err := getDeploymentSpec(ctx, mqt, routerURL, kubeClient)
	if err != nil {
		return err
	}
	_, err = kubeClient.AppsV1().Deployments(mqt.ObjectMeta.Namespace).Create(ctx, deployment, metav1.CreateOptions{})
	return err
}

func updateDeployment(ctx context.Context, mqt *fv1.MessageQueueTrigger, routerURL string, kubeClient kubernetes.Interface) error {
	deployment, err := getDeploymentSpec(ctx, mqt, routerURL, kubeClient)
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
			Triggers: []kedav1alpha1.ScaleTriggers{
				{
					Type:     string(mqt.Spec.MessageQueueType),
					Metadata: mqt.Spec.Metadata,
					AuthenticationRef: &kedav1alpha1.AuthenticationRef{
						Name: authenticationRef,
					},
				},
			},
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
