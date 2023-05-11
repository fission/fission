/*
Copyright 2019 The Fission Authors.

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

package executortype

import (
	"context"

	"go.uber.org/zap"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
)

type ExecutorType interface {
	// Run runs background job.
	Run(context.Context)

	// GetTypeName returns the name of executor type
	GetTypeName(context.Context) fv1.ExecutorType

	// GetFuncSvc specializes function pod(s) and returns a service URL for the function.
	GetFuncSvc(context.Context, *fv1.Function) (*fscache.FuncSvc, error)

	// GetFuncSvcFromCache retrieves function service from cache.
	GetFuncSvcFromCache(context.Context, *fv1.Function) (*fscache.FuncSvc, error)

	// DumpDebugInfo dump function service cache to temporary directory of executor pod.
	DumpDebugInfo(context.Context) error

	// DeleteFuncSvcFromCache deletes function service entry in cache.
	DeleteFuncSvcFromCache(context.Context, *fscache.FuncSvc)

	// TapService updates the access time of function service entry to
	// avoid idle pod reaper recycles pods.
	TapService(ctx context.Context, serviceUrl string) error

	// UnTapService updates the isActive to false
	UnTapService(ctx context.Context, key string, svcHost string)

	// IsValid returns true if a function service is valid. Different executor types
	// use distinct ways to examine the function service.
	IsValid(context.Context, *fscache.FuncSvc) bool

	// RefreshFuncPods refreshes function pods if the secrets/configmaps pods reference to get updated.
	RefreshFuncPods(context.Context, *zap.Logger, fv1.Function) error

	// AdoptOrphanResources adopts existing resources created by the deleted executor.
	AdoptExistingResources(context.Context)

	// CleanupOldExecutorObjects cleans up resources created by old executor instances
	CleanupOldExecutorObjects(context.Context)
}
