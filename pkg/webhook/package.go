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
	ctrl "sigs.k8s.io/controller-runtime"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type Package struct {
	webhook *WebhookTemplate[*v1.Package]
}

func NewPackage() *Package {
	logger := loggerfactory.GetLogger().Named("package-resource")
	return &Package{
		webhook: NewWebhookTemplate(logger, defaultPackage, validatePackage),
	}
}

func (r *Package) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return r.webhook.SetupWebhookWithManager(mgr, &v1.Package{})
}

func defaultPackage(ctx context.Context, new *v1.Package) error {
	if new.Status.BuildStatus == "" {
		if !new.Spec.Deployment.IsEmpty() {
			new.Status.BuildStatus = v1.BuildStatusNone
		} else if !new.Spec.Source.IsEmpty() {
			new.Status.BuildStatus = v1.BuildStatusPending
		} else {
			new.Status.BuildStatus = v1.BuildStatusFailed
			new.Status.BuildLog = "Both source and deployment are empty"
		}
	}
	return nil
}

func validatePackage(old, new *v1.Package) error {
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
