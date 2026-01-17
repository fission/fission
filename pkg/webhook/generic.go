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

package webhook

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Validator is an interface that can be implemented to provide custom validation logic.
type Validator[T client.Object] interface {
	Validate(T) error
}

// Defaulter is an interface that can be implemented to provide custom defaulting logic.
type Defaulter[T client.Object] interface {
	ApplyDefaults(T) error
}

// GenericWebhook implements the webhook interfaces for a generic type T.
type GenericWebhook[T client.Object] struct {
	Logger    logr.Logger
	Validator Validator[T]
	Defaulter Defaulter[T]
}

// SetupWebhookWithManager sets up the webhook with the manager.
func (w *GenericWebhook[T]) SetupWebhookWithManager(mgr ctrl.Manager, obj T) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(obj).
		WithDefaulter(w).
		WithValidator(w).
		Complete()
}

// Default implements webhook.CustomDefaulter.
func (w *GenericWebhook[T]) Default(_ context.Context, obj runtime.Object) error {
	if w.Defaulter == nil {
		return nil
	}
	typedObj, ok := obj.(T)
	if !ok {
		var t T
		return apierrors.NewBadRequest(fmt.Sprintf("expected %T but got %T", t, obj))
	}
	w.Logger.V(1).Info("default", "name", typedObj.GetName())
	return w.Defaulter.ApplyDefaults(typedObj)
}

// ValidateCreate implements webhook.CustomValidator.
func (w *GenericWebhook[T]) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	typedObj, ok := obj.(T)
	if !ok {
		var t T
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected %T but got %T", t, obj))
	}
	w.Logger.V(1).Info("validate create", "name", typedObj.GetName())
	if w.Validator != nil {
		return nil, w.Validator.Validate(typedObj)
	}
	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator.
func (w *GenericWebhook[T]) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	typedObj, ok := newObj.(T)
	if !ok {
		var t T
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected %T but got %T", t, newObj))
	}
	w.Logger.V(1).Info("validate update", "name", typedObj.GetName())
	if w.Validator != nil {
		return nil, w.Validator.Validate(typedObj)
	}
	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator.
func (w *GenericWebhook[T]) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
