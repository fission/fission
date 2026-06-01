// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"fmt"

	"github.com/dustin/go-humanize"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type Package struct {
	GenericWebhook[*v1.Package]
}

func (r *Package) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = loggerfactory.GetLogger().WithName("package-resource")
	r.Validator = r
	r.Defaulter = r
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.Package{})
}

//+kubebuilder:webhook:path=/mutate-fission-io-v1-package,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=packages,verbs=create;update,versions=v1,name=mpackage.fission.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &Package{}

// user change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-package,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=packages,verbs=create;update,versions=v1,name=vpackage.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &Package{}

func (r *Package) ApplyDefaults(new *v1.Package) error {
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

func (r *Package) Validate(new *v1.Package) error {
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

	// Refuse Package objects whose spec.environment.namespace targets a
	// different namespace than the Package itself. The buildermgr runs with
	// a cluster-wide ServiceAccount and would otherwise dispatch a build
	// into the referenced Environment's namespace builder, leaking that
	// namespace's builder-SA token and Secret/ConfigMap contents back via
	// Package.status.buildlog (GHSA-vjhc-cf4p-72q4).
	if new.Spec.Environment.Namespace != "" && new.Spec.Environment.Namespace != new.Namespace {
		return ferror.MakeError(ferror.ErrorInvalidArgument,
			fmt.Sprintf("Package.spec.environment.namespace must equal Package namespace (got env.namespace=%q, pkg.namespace=%q)",
				new.Spec.Environment.Namespace, new.Namespace))
	}
	return nil
}
