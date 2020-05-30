package mqtrigger

import (
	"context"
	"os"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"go.uber.org/zap"
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
	fissionClient, _, _, err := crd.MakeFissionClient()
	if err != nil {
		return err
	}
	crdClient := fissionClient.CoreV1().RESTClient()
	resyncPeriod := 30 * time.Second
	listWatch := k8sCache.NewListWatchFromClient(crdClient, "messagequeuetrigger", metav1.NamespaceAll, fields.Everything())
	_, controller := k8sCache.NewInformer(listWatch, &fv1.MessageQueueTrigger{}, resyncPeriod, k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			go func() {
				mqt := obj.(*fv1.MessageQueueTrigger)
				logger.Debug("Create deployment for Scaler Object", zap.Any("mqt", mqt.ObjectMeta), zap.Any("maqt.Spec", mqt.Spec))
				scaledObject := getScaledObject(mqt)
				kedaClient, err := getKedaClient(mqt.ObjectMeta.Namespace)
				if err != nil {
					logger.Error("Failed to create KEDA client", zap.Error(err))
				}
				_, err = kedaClient.Create(scaledObject, metav1.CreateOptions{})
				if err != nil {
					logger.Error("Failed to create ScaledObject", zap.Error(err))
				}
			}()

		},
		UpdateFunc: func(_ interface{}, newObj interface{}) {
			go func() {
				mqt := newObj.(*fv1.MessageQueueTrigger)
				logger.Debug("Update deployment for Scaler Object", zap.Any("mqt", mqt.ObjectMeta), zap.Any("maqt.Spec", mqt.Spec))
				scaledObject := getScaledObject(mqt)
				kedaClient, err := getKedaClient(mqt.ObjectMeta.Namespace)
				if err != nil {
					logger.Error("Failed to create KEDA client", zap.Error(err))
				}
				_, err = kedaClient.Update(scaledObject, metav1.UpdateOptions{})
				if err != nil {
					logger.Error("Failed to Update ScaledObject", zap.Error(err))
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
				}
				err = kedaClient.Delete(name, &metav1.DeleteOptions{})
				if err != nil {
					logger.Error("Failed to Delete ScaledObject", zap.Error(err))
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
