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
	"fmt"

	"github.com/dustin/go-humanize"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	ferror "github.com/fission/fission/pkg/error"
)

// log is for logging in this package.
var packagelog = logf.Log.WithName("package-resource")

func (r *Package) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

//+kubebuilder:webhook:path=/mutate-fission-io-v1-package,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=packages,verbs=create;update,versions=v1,name=mpackage.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &Package{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *Package) Default() {
	packagelog.Info("default", "name", r.Name)

	// TODO(user): fill in your defaulting logic.
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-package,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=packages,verbs=create;update,versions=v1,name=vpackage.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &Package{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *Package) ValidateCreate() error {
	packagelog.Info("validate create", "name", r.Name)
	err := r.Validate()
	if err != nil {
		err = AggregateValidationErrors("Package", err)
		return err
	}

	// Ensure size limits
	if len(r.Spec.Source.Literal) > int(ArchiveLiteralSizeLimit) {
		err := ferror.MakeError(ferror.ErrorInvalidArgument,
			fmt.Sprintf("Package literal larger than %s", humanize.Bytes(uint64(ArchiveLiteralSizeLimit))))
		return err
	}
	if len(r.Spec.Deployment.Literal) > int(ArchiveLiteralSizeLimit) {
		err := ferror.MakeError(ferror.ErrorInvalidArgument,
			fmt.Sprintf("Package literal larger than %s", humanize.Bytes(uint64(ArchiveLiteralSizeLimit))))
		return err
	}
	return nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *Package) ValidateUpdate(old runtime.Object) error {
	packagelog.Info("validate update", "name", r.Name)
	err := r.Validate()

	if err != nil {
		err = AggregateValidationErrors("Package", err)
		return err
	}

	return nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *Package) ValidateDelete() error {
	packagelog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil
}
