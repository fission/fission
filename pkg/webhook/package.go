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

	"github.com/dustin/go-humanize"
	"go.uber.org/zap"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type Package struct{}

// log is for logging in this package.
var packagelog = loggerfactory.GetLogger().Named("package-resource")

func (r *Package) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&v1.Package{}).
		WithDefaulter(r).
		WithValidator(r).
		Complete()
}

//+kubebuilder:webhook:path=/mutate-fission-io-v1-package,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=packages,verbs=create;update,versions=v1,name=mpackage.fission.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &Package{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (r *Package) Default(_ context.Context, obj runtime.Object) error {
	new, ok := obj.(*v1.Package)
	if !ok {
		return apierrors.NewBadRequest(fmt.Sprintf("expected a Package but got a %T", obj))
	}
	packagelog.Debug("default", zap.String("name", new.Name))
	if new.Status.BuildStatus == "" {
		if !new.Spec.Deployment.IsEmpty() {
			// deployment package exists
			new.Status.BuildStatus = v1.BuildStatusNone
		} else if !new.Spec.Source.IsEmpty() {
			// source package with no deployment is a pending build
			new.Status.BuildStatus = v1.BuildStatusPending
		} else {
			new.Status.BuildStatus = v1.BuildStatusFailed // empty package
			new.Status.BuildLog = "Both source and deployment are empty"
		}
	}
	return nil
}

// user change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-package,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=packages,verbs=create;update,versions=v1,name=vpackage.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &Package{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (r *Package) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	new, ok := obj.(*v1.Package)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a Package but got a %T", obj))
	}
	packagelog.Debug("validate create", zap.String("name", new.Name))
	return nil, r.validate(nil, new)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (r *Package) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	new, ok := newObj.(*v1.Package)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a Package but got a %T", newObj))
	}
	packagelog.Debug("validate update", zap.String("name", new.Name))
	return nil, r.validate(nil, new)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (r *Package) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (r *Package) validate(_ *v1.Package, new *v1.Package) error {
	err := new.Validate()
	if err != nil {
		return v1.AggregateValidationErrors("Package", err)
	}

	// Ensure size limits
	if len(new.Spec.Source.Literal) > int(v1.ArchiveLiteralSizeLimit) {
		return ferror.MakeError(ferror.ErrorInvalidArgument,
			fmt.Sprintf("Package literal larger than %s", humanize.Bytes(uint64(v1.ArchiveLiteralSizeLimit))))
	}
	if len(new.Spec.Deployment.Literal) > int(v1.ArchiveLiteralSizeLimit) {
		return ferror.MakeError(ferror.ErrorInvalidArgument,
			fmt.Sprintf("Package literal larger than %s", humanize.Bytes(uint64(v1.ArchiveLiteralSizeLimit))))
	}
	return nil
}
