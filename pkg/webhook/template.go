package webhook

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
)

// ResourceValidator defines interface for resource specific validation
type ResourceValidator interface {
	Validate() error
	GetName() string
}

// WebhookTemplate provides generic implementation for webhooks
type WebhookTemplate[T any] struct {
	logger *zap.Logger
	// Resource specific handlers
	defaulter func(context.Context, T) error
	validator func(T, T) error
}

// NewWebhookTemplate creates new webhook template instance
func NewWebhookTemplate[T any](logger *zap.Logger, defaulter func(context.Context, T) error, validator func(T, T) error) *WebhookTemplate[T] {
	return &WebhookTemplate[T]{
		logger:    logger,
		defaulter: defaulter,
		validator: validator,
	}
}

// SetupWebhookWithManager sets up the webhook with manager
func (w *WebhookTemplate[T]) SetupWebhookWithManager(mgr ctrl.Manager, obj runtime.Object) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(obj).
		WithDefaulter(w).
		WithValidator(w).
		Complete()
}

// Default implements webhook.CustomDefaulter
func (w *WebhookTemplate[T]) Default(ctx context.Context, obj runtime.Object) error {
	resource, ok := obj.(T)
	if !ok {
		return apierrors.NewBadRequest(fmt.Sprintf("expected a %T but got a %T", *new(T), obj))
	}
	if w.defaulter != nil {
		return w.defaulter(ctx, resource)
	}
	return nil
}

// ValidateCreate implements webhook.CustomValidator
func (w *WebhookTemplate[T]) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	resource, ok := obj.(T)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a %T but got a %T", *new(T), obj))
	}
	w.logger.Debug("validate create")
	var old T
	return nil, w.validator(old, resource)
}

// ValidateUpdate implements webhook.CustomValidator
func (w *WebhookTemplate[T]) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldResource, ok := oldObj.(T)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a %T but got a %T", *new(T), oldObj))
	}
	newResource, ok := newObj.(T)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a %T but got a %T", *new(T), newObj))
	}
	w.logger.Debug("validate update")
	return nil, w.validator(oldResource, newResource)
}

// ValidateDelete implements webhook.CustomValidator
func (w *WebhookTemplate[T]) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

var _ webhook.CustomDefaulter = &WebhookTemplate[*v1.Function]{}
var _ webhook.CustomValidator = &WebhookTemplate[*v1.Function]{}
