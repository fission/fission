/*
Copyright 2022.

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
	"context"
	"fmt"
	"os"

	"go.uber.org/zap"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// log is for logging in this package.
var functionlog = loggerfactory.GetLogger().Named("function-resource")

func (r *Function) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-function,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=functions,verbs=create;update,versions=v1,name=mfunction.fission.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &Function{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *Function) Default() {
}

// user change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-function,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=functions,verbs=create;update,versions=v1,name=vfunction.fission.io,admissionReviewVersions=v1

var _ webhook.Validator = &Function{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *Function) ValidateCreate() error {
	functionlog.Debug("validate create", zap.String("name", r.Name))
	var clientset kubernetes.Interface
	var err error
	if len(r.Spec.ConfigMaps) > 0 || len(r.Spec.Secrets) > 0 {
		_, clientset, err = GetKubernetesClient()
		if err != nil {
			return err
		}
	}

	for _, cnfMap := range r.Spec.ConfigMaps {
		_, err := clientset.CoreV1().ConfigMaps(r.ObjectMeta.Namespace).Get(context.TODO(), cnfMap.Name, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				err := fmt.Errorf("ConfigMap %s not found in Namespace: %s. ConfigMap needs to be present in the same namespace as function", cnfMap.Name, r.ObjectMeta.Namespace)
				return AggregateValidationErrors("Function", err)
			} else {
				err := fmt.Errorf("error checking configmap %v %s", err, cnfMap.Name)
				return AggregateValidationErrors("Function", err)
			}
		}
	}

	for _, secretName := range r.Spec.Secrets {
		_, err := clientset.CoreV1().Secrets(r.ObjectMeta.Namespace).Get(context.TODO(), secretName.Name, metav1.GetOptions{})

		if err != nil {
			if k8serrors.IsNotFound(err) {
				err := fmt.Errorf("secret %s not found in Namespace: %s. Secret needs to be present in the same namespace as function", secretName, r.ObjectMeta.Namespace)
				return AggregateValidationErrors("Function", err)
			} else {
				err := fmt.Errorf("error checking secret %s. err: %v", secretName, err)
				return AggregateValidationErrors("Function", err)
			}
		}
	}

	err = r.Validate()
	if err != nil {
		return AggregateValidationErrors("Function", err)
	}
	return nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *Function) ValidateUpdate(old runtime.Object) error {
	functionlog.Debug("validate update", zap.String("name", r.Name))

	var clientset kubernetes.Interface
	var err error
	if len(r.Spec.ConfigMaps) > 0 || len(r.Spec.Secrets) > 0 {
		_, clientset, err = GetKubernetesClient()
		if err != nil {
			return err
		}
	}

	for _, cnfMap := range r.Spec.ConfigMaps {
		_, err := clientset.CoreV1().ConfigMaps(r.ObjectMeta.Namespace).Get(context.TODO(), cnfMap.Name, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				err := fmt.Errorf("ConfigMap %s not found in Namespace: %s. ConfigMap needs to be present in the same namespace as function", cnfMap.Name, r.ObjectMeta.Namespace)
				return AggregateValidationErrors("Function", err)
			} else {
				err := fmt.Errorf("error checking configmap %v %s", err, cnfMap.Name)
				return AggregateValidationErrors("Function", err)
			}
		}
	}

	for _, secretName := range r.Spec.Secrets {
		_, err := clientset.CoreV1().Secrets(r.ObjectMeta.Namespace).Get(context.TODO(), secretName.Name, metav1.GetOptions{})

		if err != nil {
			if k8serrors.IsNotFound(err) {
				err := fmt.Errorf("secret %s not found in Namespace: %s. Secret needs to be present in the same namespace as function", secretName.Name, r.ObjectMeta.Namespace)
				return AggregateValidationErrors("Function", err)
			} else {
				err := fmt.Errorf("error checking secret %s. err: %v", secretName, err)
				return AggregateValidationErrors("Function", err)
			}
		}
	}
	err = r.Validate()
	if err != nil {
		return AggregateValidationErrors("Function", err)
	}
	return nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *Function) ValidateDelete() error {
	functionlog.Debug("validate delete", zap.String("name", r.Name))
	return nil
}

func GetKubernetesClient() (*rest.Config, kubernetes.Interface, error) {
	var config *rest.Config
	var err error

	// get the config, either from kubeconfig or using our
	// in-cluster service account
	kubeConfig := os.Getenv("KUBECONFIG")
	if len(kubeConfig) != 0 {
		config, err = clientcmd.BuildConfigFromFlags("", kubeConfig)
		if err != nil {
			return nil, nil, err
		}
	} else {
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, nil, err
		}
	}

	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}

	return config, clientset, nil
}
