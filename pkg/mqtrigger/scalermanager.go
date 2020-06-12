package mqtrigger

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	k8sCache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	kedaGVR = schema.GroupVersionResource{
		Group:    "keda.k8s.io",
		Version:  "v1alpha1",
		Resource: "scaledobjects",
	}
)

func getKedaClient(namespace string) (dynamic.ResourceInterface, error) {
	var config *rest.Config
	var err error

	// get the config, either from kubeconfig or using our
	// in-cluster service account
	kubeConfig := os.Getenv("KUBECONFIG")
	if len(kubeConfig) != 0 {
		config, err = clientcmd.BuildConfigFromFlags("", kubeConfig)
		if err != nil {
			return nil, err
		}
	} else {
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, err
		}
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return dynamicClient.Resource(kedaGVR).Namespace(namespace), nil
}

func StartScalerManager(logger *zap.Logger, routerUrl string) error {
	fissionClient, kubeClient, _, err := crd.MakeFissionClient()
	if err != nil {
		return err
	}
	err = fissionClient.WaitForCRDs()
	if err != nil {
		return errors.Wrap(err, "error waiting for CRDs")
	}
	crdClient := fissionClient.CoreV1().RESTClient()
	deploymentsClient := kubeClient.AppsV1().Deployments(apiv1.NamespaceDefault)
	resyncPeriod := 30 * time.Second
	listWatch := k8sCache.NewListWatchFromClient(crdClient, "messagequeuetriggers", metav1.NamespaceAll, fields.Everything())
	_, controller := k8sCache.NewInformer(listWatch, &fv1.MessageQueueTrigger{}, resyncPeriod, k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			go func() {
				mqt := obj.(*fv1.MessageQueueTrigger)
				logger.Debug("Create deployment for Scaler Object", zap.Any("mqt", mqt.ObjectMeta), zap.Any("mqt.Spec", mqt.Spec))
				scaledObject := getScaledObject(mqt)
				kedaClient, err := getKedaClient(mqt.ObjectMeta.Namespace)
				if err != nil {
					logger.Error("Failed to create KEDA client", zap.Error(err))
					return
				}
				_, err = kedaClient.Create(scaledObject, metav1.CreateOptions{})
				if err != nil {
					logger.Error("Failed to create ScaledObject", zap.Error(err))
					return
				}
				deployment := getDeploymentSpec(mqt, routerUrl)
				_, err = deploymentsClient.Create(deployment)
				if err != nil {
					logger.Error("Failed to create deployment", zap.Error(err))
					return
				}
			}()

		},
		UpdateFunc: func(obj interface{}, newObj interface{}) {
			go func() {
				mqt := obj.(*fv1.MessageQueueTrigger)
				newMqt := newObj.(*fv1.MessageQueueTrigger)
				updated := checkAndUpdateTriggerFields(mqt, newMqt)
				if !updated {
					logger.Warn("Update failed, no changes found in trigger fields")
					return
				}
				kedaClient, err := getKedaClient(mqt.ObjectMeta.Namespace)
				if err != nil {
					logger.Error("Failed to create KEDA client", zap.Error(err))
					return
				}
				scaledObject := getScaledObject(mqt)
				resourceVersion, err := getResourceVersion(mqt.ObjectMeta.Name, kedaClient)
				if err != nil {
					logger.Error("Failed to get resource version", zap.Error(err))
					return
				}
				scaledObject.SetResourceVersion(resourceVersion)
				_, err = kedaClient.Update(scaledObject, metav1.UpdateOptions{})
				if err != nil {
					logger.Error("Failed to Update ScaledObject", zap.Error(err))
					return
				}
			}()
		},
		DeleteFunc: func(obj interface{}) {
			go func() {
				mqt := obj.(*fv1.MessageQueueTrigger)
				logger.Debug("Delete deployment for Scaler Object", zap.Any("mqt", mqt.ObjectMeta), zap.Any("maqt.Spec", mqt.Spec))
				name := mqt.ObjectMeta.Name
				kedaClient, err := getKedaClient(mqt.ObjectMeta.Namespace)
				if err != nil {
					logger.Error("Failed to create KEDA client", zap.Error(err))
					return
				}
				err = kedaClient.Delete(name, &metav1.DeleteOptions{})
				if err != nil {
					logger.Error("Failed to Delete ScaledObject", zap.Error(err))
					return
				}
				deletePolicy := metav1.DeletePropagationForeground
				if err := deploymentsClient.Delete(mqt.ObjectMeta.Name, &metav1.DeleteOptions{
					PropagationPolicy: &deletePolicy,
				}); err != nil {
					logger.Error("Failed to Delete Deployment", zap.Error(err))
					return
				}
			}()
		},
	})
	controller.Run(context.Background().Done())
	return nil
}

func getScaledObject(mqt *fv1.MessageQueueTrigger) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"kind":       "ScaledObject",
			"apiVersion": "keda.k8s.io/v1alpha1",
			"metadata": map[string]interface{}{
				"name":      mqt.ObjectMeta.Name,
				"namespace": mqt.ObjectMeta.Namespace,
			},
			"spec": map[string]interface{}{
				"cooldownPeriod":  &mqt.Spec.CooldownPeriod,
				"maxReplicaCount": &mqt.Spec.MaxReplicaCount,
				"minReplicaCount": &mqt.Spec.MinReplicaCount,
				"pollingInterval": &mqt.Spec.PollingInterval,
				"scaleTargetRef": map[string]interface{}{
					"deploymentName": mqt.ObjectMeta.Name,
				},
				"triggers": []interface{}{
					map[string]interface{}{
						"type":     mqt.ObjectMeta.Name,
						"metadata": mqt.Spec.Metadata,
						"authdata": mqt.Spec.Authdata,
					},
				},
			},
		},
	}
}

func getDeploymentSpec(mqt *fv1.MessageQueueTrigger, routerUrl string) *appsv1.Deployment {
	url := routerUrl + "/" + strings.TrimPrefix(utils.UrlForFunction(mqt.Spec.FunctionReference.Name, mqt.ObjectMeta.Namespace), "/")
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: mqt.ObjectMeta.Name,
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
				Spec: apiv1.PodSpec{
					Containers: []apiv1.Container{
						{
							Name:            mqt.ObjectMeta.Name,
							Image:           "rahulbhati/test:3",
							ImagePullPolicy: "Always",
							Env: []apiv1.EnvVar{
								{
									Name:  "BROKER_LIST",
									Value: mqt.Spec.Metadata["brokerList"],
								},
								{
									Name:  "BOOTSTRAP_SERVERS",
									Value: mqt.Spec.Metadata["bootstrapServers"],
								},
								{
									Name:  "CONSUMER_GROUP",
									Value: mqt.Spec.Metadata["consumerGroup"],
								},
								{
									Name:  "TOPIC",
									Value: mqt.Spec.Topic,
								},
								{
									Name:  "LAG_THRESHOLD",
									Value: mqt.Spec.Metadata["lagThreshold"],
								},
								{
									Name:  "AUTH_MODE",
									Value: mqt.Spec.Metadata["authMode"],
								},
								{
									Name:  "USERNAME",
									Value: mqt.Spec.Metadata["username"],
								},
								{
									Name:  "PASSWORD",
									Value: mqt.Spec.Metadata["password"],
								},
								{
									Name:  "CA",
									Value: mqt.Spec.Metadata["ca"],
								},
								{
									Name:  "CERT",
									Value: mqt.Spec.Metadata["cert"],
								},
								{
									Name:  "KEY",
									Value: mqt.Spec.Metadata["key"],
								},
								{
									Name:  "FUNCTION_URL",
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
									Name:  "TRIGGER_NAME",
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
							},
						},
					},
				},
			},
		},
	}
}

func getResourceVersion(scaledObjectName string, kedaClient dynamic.ResourceInterface) (version string, err error) {
	scaledObject, err := kedaClient.Get(scaledObjectName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return scaledObject.GetResourceVersion(), nil
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

	for key, value := range newMqt.Spec.Metadata {
		if val, ok := mqt.Spec.Metadata[key]; ok && val != value {
			mqt.Spec.Metadata[key] = value
			updated = true
		}
	}

	for key, value := range newMqt.Spec.Authdata {
		if val, ok := mqt.Spec.Authdata[key]; ok && val != value {
			mqt.Spec.Authdata[key] = value
			updated = true
		}
	}
	return updated
}
