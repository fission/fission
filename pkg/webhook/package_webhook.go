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
	"fmt"

	"github.com/dustin/go-humanize"
	"go.uber.org/zap"
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
		Complete()
}

//+kubebuilder:webhook:path=/mutate-fission-io-v1-package,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=packages,verbs=create;update,versions=v1,name=mpackage.fission.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &Package{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *Package) Default() {
	packagelog.Debug("default", zap.String("name", r.Name))
	if r.Status.BuildStatus == "" {
		if !r.Spec.Deployment.IsEmpty() {
			// deployment package exists
			r.Status.BuildStatus = BuildStatusNone
		} else if !r.Spec.Source.IsEmpty() {
			// source package with no deployment is a pending build
			r.Status.BuildStatus = BuildStatusPending
		} else {
			r.Status.BuildStatus = BuildStatusFailed // empty package
			r.Status.BuildLog = "Both source and deployment are empty"
		}
	}
}

// user change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-package,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=packages,verbs=create;update,versions=v1,name=vpackage.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &Package{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *Package) ValidateCreate() (admission.Warnings, error) {
	packagelog.Debug("validate create", zap.String("name", r.Name))
	err := r.Validate()
	if err != nil {
		err = AggregateValidationErrors("Package", err)
		return nil, err
	}

	// Ensure size limits
	if len(r.Spec.Source.Literal) > int(ArchiveLiteralSizeLimit) {
		err := ferror.MakeError(ferror.ErrorInvalidArgument,
			fmt.Sprintf("Package literal larger than %s", humanize.Bytes(uint64(ArchiveLiteralSizeLimit))))
		return nil, err
	}
	if len(r.Spec.Deployment.Literal) > int(ArchiveLiteralSizeLimit) {
		err := ferror.MakeError(ferror.ErrorInvalidArgument,
			fmt.Sprintf("Package literal larger than %s", humanize.Bytes(uint64(ArchiveLiteralSizeLimit))))
		return nil, err
	}
	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *Package) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	packagelog.Debug("validate update", zap.String("name", r.Name))
	err := r.Validate()

	if err != nil {
		err = AggregateValidationErrors("Package", err)
		return nil, err
	}

	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *Package) ValidateDelete() (admission.Warnings, error) {
	packagelog.Debug("validate delete", zap.String("name", r.Name))
	return nil, nil
}
