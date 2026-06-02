// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"

	"github.com/go-logr/logr"
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
	return ctrl.NewWebhookManagedBy(mgr, obj).
		WithDefaulter(w).
		WithValidator(w).
		Complete()
}

// Default implements admission.Defaulter.
func (w *GenericWebhook[T]) Default(_ context.Context, obj T) error {
	if w.Defaulter == nil {
		return nil
	}
	w.Logger.V(1).Info("default", "name", obj.GetName())
	return w.Defaulter.ApplyDefaults(obj)
}

// ValidateCreate implements admission.Validator.
func (w *GenericWebhook[T]) ValidateCreate(_ context.Context, obj T) (admission.Warnings, error) {
	w.Logger.V(1).Info("validate create", "name", obj.GetName())
	if w.Validator != nil {
		return nil, w.Validator.Validate(obj)
	}
	return nil, nil
}

// ValidateUpdate implements admission.Validator.
func (w *GenericWebhook[T]) ValidateUpdate(_ context.Context, _, newObj T) (admission.Warnings, error) {
	w.Logger.V(1).Info("validate update", "name", newObj.GetName())
	if w.Validator != nil {
		return nil, w.Validator.Validate(newObj)
	}
	return nil, nil
}

// ValidateDelete implements admission.Validator.
func (w *GenericWebhook[T]) ValidateDelete(_ context.Context, _ T) (admission.Warnings, error) {
	return nil, nil
}
