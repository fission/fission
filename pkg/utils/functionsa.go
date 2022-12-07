package utils

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-multierror"
	authorizationv1 "k8s.io/api/authorization/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
)

const (
	FetcherSAName = "fission-fetcher"
	BuilderSAName = "fission-builder"
)

type PermissionCheck struct {
	gvr  schema.GroupVersionResource
	verb string
}

type ServiceAccountPermissionCheck struct {
	sa          string
	permissions []PermissionCheck
}

func CheckServiceAccountExistsWithPermissions(ctx context.Context, client kubernetes.Interface) error {
	var fetcherCheck = ServiceAccountPermissionCheck{
		sa: FetcherSAName,
		permissions: []PermissionCheck{
			{
				gvr:  schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"},
				verb: "list",
			},
			{
				gvr:  schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"},
				verb: "get",
			},
			{
				gvr:  schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"},
				verb: "get",
			},
			{
				gvr:  schema.GroupVersionResource{Group: "fission.io", Version: "v1", Resource: "packages"},
				verb: "get",
			},
			{
				gvr:  schema.GroupVersionResource{Group: "", Version: "v1", Resource: "events"},
				verb: "create",
			},
		},
	}
	var builderCheck = ServiceAccountPermissionCheck{
		sa: BuilderSAName,
		permissions: []PermissionCheck{
			{
				gvr:  schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"},
				verb: "get",
			},
			{
				gvr:  schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"},
				verb: "get",
			},
			{
				gvr:  schema.GroupVersionResource{Group: "fission.io", Version: "v1", Resource: "packages"},
				verb: "get",
			},
		},
	}

	nsResolver = DefaultNSResolver()
	result := &multierror.Error{}
	// Check if fetcher service account exists in function namespace
	for _, ns := range nsResolver.FissionResourceNS {
		sa, err := client.CoreV1().ServiceAccounts(nsResolver.GetBuilderNS(ns)).Get(ctx, fetcherCheck.sa, metav1.GetOptions{})
		if err != nil {
			result = multierror.Append(result, err)
			continue
		}
		err = CheckPermissions(ctx, client, sa, fetcherCheck.permissions)
		if err != nil {
			result = multierror.Append(result, err)
		}
	}

	// Check if builder service account exists in builder namespace
	for _, ns := range nsResolver.FissionResourceNS {
		sa, err := client.CoreV1().ServiceAccounts(nsResolver.GetFunctionNS(ns)).Get(ctx, builderCheck.sa, metav1.GetOptions{})
		if err != nil {
			result = multierror.Append(result, err)
			continue
		}
		err = CheckPermissions(ctx, client, sa, builderCheck.permissions)
		if err != nil {
			result = multierror.Append(result, err)
		}
	}
	return result.ErrorOrNil()
}

func CheckPermissions(ctx context.Context, client kubernetes.Interface, sa *v1.ServiceAccount, permissions []PermissionCheck) error {
	result := &multierror.Error{}
	for _, permission := range permissions {
		err := CheckPermission(ctx, client, sa, permission.gvr, permission.verb)
		if err != nil {
			result = multierror.Append(result, err)
		}
		result = multierror.Append(result, err)
	}
	return result.ErrorOrNil()
}

// Check permission with SubjectAccessReview
func CheckPermission(ctx context.Context, client kubernetes.Interface, sa *v1.ServiceAccount, gvr schema.GroupVersionResource, verb string) error {
	user := fmt.Sprintf("system:serviceaccount:%s:%s", sa.Namespace, sa.Name)
	sar := authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: sa.Namespace,
				Group:     gvr.Group,
				Version:   gvr.Version,
				Resource:  gvr.Resource,
				Verb:      verb,
			},
			User: fmt.Sprintf("system:serviceaccount:%s:%s", sa.Namespace, sa.Name),
		},
	}
	r, err := client.AuthorizationV1().SubjectAccessReviews().Create(ctx, &sar, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	if !r.Status.Allowed {
		return fmt.Errorf("permission %s/%s/%s-%s denied for sa %s", gvr.Group, gvr.Version, gvr.Resource, verb, user)
	}
	return nil
}
