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

	"github.com/pkg/errors"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/util"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/manager"
	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	kedaClient "github.com/kedacore/keda/v2/pkg/generated/clientset/versioned"
)

var (
	matchFirstCap = regexp.MustCompile("(.)([A-Z][a-z]+)")
	matchAllCap   = regexp.MustCompile("([a-z0-9])([A-Z])")
)

func mqTriggerEventHandlers(ctx context.Context, logger *zap.Logger, kubeClient kubernetes.Interface, kedaClient kedaClient.Interface, routerURL string) k8sCache.ResourceEventHandlerFuncs {
	return k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			go func() {
				mqt := obj.(*fv1.MessageQueueTrigger)
				if mqt.Spec.MqtKind == "fission" {
					return
				}
				logger.Debug("Create deployment for Scaler Object", zap.Any("mqt", mqt.ObjectMeta), zap.Any("mqt.Spec", mqt.Spec))

				authenticationRef := ""
				if len(mqt.Spec.Secret) > 0 {
					authenticationRef = fmt.Sprintf("%s-auth-trigger", mqt.ObjectMeta.Name)
					err := createAuthTrigger(ctx, kedaClient, mqt, authenticationRef, kubeClient)
					if err != nil {
						logger.Error("Failed to create Authentication Trigger", zap.Error(err))
						return
					}
				}

				if err := createDeployment(ctx, mqt, routerURL, kubeClient); err != nil {
					logger.Error("Failed to create Deployment", zap.Error(err))
					if len(authenticationRef) > 0 {
						err = deleteAuthTrigger(ctx, kedaClient, authenticationRef, mqt.ObjectMeta.Namespace)
						if err != nil {
							logger.Error("Failed to delete Authentication Trigger", zap.Error(err))
						}
					}
					return
				}

				if err := createScaledObject(ctx, kedaClient, mqt, authenticationRef); err != nil {
					logger.Error("Failed to create ScaledObject", zap.Error(err))
					if len(authenticationRef) > 0 {
						if err = deleteAuthTrigger(ctx, kedaClient, authenticationRef, mqt.ObjectMeta.Namespace); err != nil {
							logger.Error("Failed to delete Authentication Trigger", zap.Error(err))
						}
					}
					if err = deleteDeployment(ctx, mqt.ObjectMeta.Name, mqt.ObjectMeta.Namespace, kubeClient); err != nil {
						logger.Error("Failed to delete Deployment", zap.Error(err))
					}
				}
			}()
		},
		UpdateFunc: func(obj interface{}, newObj interface{}) {
			go func() {
				mqt := obj.(*fv1.MessageQueueTrigger)
				newMqt := newObj.(*fv1.MessageQueueTrigger)
				updated := checkAndUpdateTriggerFields(mqt, newMqt)
				if mqt.Spec.MqtKind == "fission" {
					return
				}
				if !updated {
					logger.Warn(fmt.Sprintf("%s remains unchanged. No changes found in trigger fields", mqt.ObjectMeta.Name))
					return
				}

				authenticationRef := ""
				if len(newMqt.Spec.Secret) > 0 && newMqt.Spec.Secret != mqt.Spec.Secret {
					authenticationRef = fmt.Sprintf("%s-auth-trigger", mqt.ObjectMeta.Name)
					if err := updateAuthTrigger(ctx, kedaClient, mqt, authenticationRef, kubeClient); err != nil {
						logger.Error("Failed to update Authentication Trigger", zap.Error(err))
						return
					}
				}

				if err := updateDeployment(ctx, mqt, routerURL, kubeClient); err != nil {
					logger.Error("Failed to Update Deployment", zap.Error(err))
					return
				}

				if err := updateScaledObject(ctx, kedaClient, mqt, authenticationRef); err != nil {
					logger.Error("Failed to Update ScaledObject", zap.Error(err))
					return
				}
			}()
		},
	}

}

// StartScalerManager watches for changes in MessageQueueTrigger and,
// Based on changes, it Creates, Updates and Deletes Objects of Kind ScaledObjects, AuthenticationTriggers and Deployments
func StartScalerManager(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger *zap.Logger, mgr manager.Interface, routerURL string) error {
	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return errors.Wrap(err, "failed to get fission client")
	}
	kubeClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return errors.Wrap(err, "failed to get kubernetes client")
	}
	kedaClient, err := clientGen.GetKedaClient()
	if err != nil {
		return errors.Wrap(err, "failed to get keda client")
	}

	err = crd.WaitForFunctionCRDs(ctx, logger, fissionClient)
	if err != nil {
		return errors.Wrap(err, "error waiting for CRDs")
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
			logger.Fatal("failed to wait for caches to sync")
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
	url := routerURL + "/" + strings.TrimPrefix(utils.UrlForFunction(mqt.Spec.FunctionReference.Name, mqt.ObjectMeta.Namespace), "/")
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
			Value: mqt.ObjectMeta.Name,
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

	blockOwnerDeletion := true
	authTriggerObj := &kedav1alpha1.TriggerAuthentication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      authenticationRef,
			Namespace: mqt.ObjectMeta.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind:               "MessageQueueTrigger",
					APIVersion:         "fission.io/v1",
					Name:               mqt.ObjectMeta.Name,
					UID:                mqt.ObjectMeta.UID,
					BlockOwnerDeletion: &blockOwnerDeletion,
				},
			},
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
	if err != nil {
		return err
	}
	return nil
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
	if err != nil {
		return err
	}
	return nil
}

func deleteAuthTrigger(ctx context.Context, client kedaClient.Interface, name, namespace string) error {
	err := client.KedaV1alpha1().TriggerAuthentications(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return err
	}
	return nil
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
				Name:            mqt.ObjectMeta.Name,
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

	blockOwnerDeletion := true
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: mqt.ObjectMeta.Name,
			Labels: map[string]string{
				"app": mqt.ObjectMeta.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind:               "MessageQueueTrigger",
					APIVersion:         "fission.io/v1",
					Name:               mqt.ObjectMeta.Name,
					UID:                mqt.ObjectMeta.UID,
					BlockOwnerDeletion: &blockOwnerDeletion,
				},
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": mqt.ObjectMeta.Name,
				},
			},
			Template: apiv1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": mqt.ObjectMeta.Name,
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
	if err != nil {
		return err
	}
	return nil
}

func updateDeployment(ctx context.Context, mqt *fv1.MessageQueueTrigger, routerURL string, kubeClient kubernetes.Interface) error {
	deployment, err := getDeploymentSpec(ctx, mqt, routerURL, kubeClient)
	if err != nil {
		return err
	}
	_, err = kubeClient.AppsV1().Deployments(mqt.ObjectMeta.Namespace).Update(ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return nil
}

func deleteDeployment(ctx context.Context, name string, namespace string, kubeClient kubernetes.Interface) error {
	deletePolicy := metav1.DeletePropagationForeground
	if err := kubeClient.AppsV1().Deployments(namespace).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}); err != nil {
		return err
	}
	return nil
}

func getScaledObject(mqt *fv1.MessageQueueTrigger, authenticationRef string) *kedav1alpha1.ScaledObject {
	blockOwnerDeletion := true

	return &kedav1alpha1.ScaledObject{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mqt.ObjectMeta.Name,
			Namespace: mqt.ObjectMeta.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind:               "MessageQueueTrigger",
					APIVersion:         "fission.io/v1",
					Name:               mqt.ObjectMeta.Name,
					UID:                mqt.ObjectMeta.UID,
					BlockOwnerDeletion: &blockOwnerDeletion,
				},
			},
		},

		Spec: kedav1alpha1.ScaledObjectSpec{
			CooldownPeriod:  mqt.Spec.CooldownPeriod,
			MaxReplicaCount: mqt.Spec.MaxReplicaCount,
			MinReplicaCount: mqt.Spec.MinReplicaCount,
			PollingInterval: mqt.Spec.PollingInterval,
			ScaleTargetRef: &kedav1alpha1.ScaleTarget{
				Name: mqt.ObjectMeta.Name,
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
	if err != nil {
		return err
	}
	return nil
}

func updateScaledObject(ctx context.Context, client kedaClient.Interface, mqt *fv1.MessageQueueTrigger, authenticationRef string) error {
	oldScaledObject, err := client.KedaV1alpha1().ScaledObjects(mqt.ObjectMeta.Namespace).Get(ctx, mqt.ObjectMeta.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	resourceVersion := oldScaledObject.GetResourceVersion()

	scaledObject := getScaledObject(mqt, authenticationRef)
	scaledObject.SetResourceVersion(resourceVersion)

	_, err = client.KedaV1alpha1().ScaledObjects(mqt.ObjectMeta.Namespace).Update(ctx, scaledObject, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return nil
}
