package utils

import (
	"context"
	"fmt"

	uuid "github.com/satori/go.uuid"

	"go.uber.org/zap"
	authorizationv1 "k8s.io/api/authorization/v1"
	v1 "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
)

const (
	FetcherSAName string = "fission-fetcher"
	BuilderSAName string = "fission-builder"
)

type PermissionCheck struct {
	gvr  schema.GroupVersionResource
	verb string
}

type ServiceAccountPermissionCheck struct {
	sa          string
	permissions []PermissionCheck
}

var (
	fetcherCheck = ServiceAccountPermissionCheck{
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
				gvr:  schema.GroupVersionResource{Group: "fission.io", Version: "v1", Resource: "events"},
				verb: "create",
			},
		},
	}
	builderCheck = ServiceAccountPermissionCheck{
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
)

func SetupSAAndRoleBindings(ctx context.Context, client kubernetes.Interface, logger *zap.Logger, SAName string, namespace string) {
	if saObj, err := IsSAExists(ctx, client, SAName, namespace); err == nil {
		// if SA exists then check for permission and create role and rolebinding accordingly
		logger.Debug("SA exists",
			zap.String("SA_name", saObj.Name),
			zap.String("namespace", saObj.Namespace))
		if SAName == BuilderSAName {
			CheckPermissions(ctx, client, logger, saObj, builderCheck.permissions)
		} else {
			CheckPermissions(ctx, client, logger, saObj, fetcherCheck.permissions)
		}
	} else if err != nil && k8serrors.IsNotFound(err) {
		// if SA not exists then create SA first
		if sa, err := SetupSA(ctx, client, SAName, namespace); err != nil {
			// return if got any error while creating SA
			logger.Error("error while creating service account",
				zap.String("SA Name", SAName),
				zap.String("namespace", namespace))
			return
		} else {
			// service account created successfully, setup role and rolebinding for newly created SA
			// we don't need to check permissions for newly created SA.
			logger.Debug("SA created successfully, now creating role and rolebinding",
				zap.String("SA name", sa.Name),
				zap.String("SA namespace", sa.Namespace))
			if SAName == BuilderSAName {
				for _, permission := range builderCheck.permissions {
					SetupRoleAndBinding(ctx, client, logger, permission, sa)
				}
			} else {
				for _, permission := range fetcherCheck.permissions {
					SetupRoleAndBinding(ctx, client, logger, permission, sa)
				}
			}
		}
	} else {
		logger.Error("error occured while getting service account",
			zap.String("SA Name", SAName),
			zap.String("namespace", namespace))
		return
	}
}

func CheckPermissions(ctx context.Context, client kubernetes.Interface, logger *zap.Logger, sa *v1.ServiceAccount, permissions []PermissionCheck) {
	logger.Debug("checking SA permission",
		zap.String("SA_Name", sa.Name),
		zap.String("namespace", sa.Namespace))

	for _, permission := range permissions {
		isPermissionExists, err := CheckPermission(ctx, client, sa, permission.gvr, permission.verb)
		if err != nil {
			// some error occure while checking permission, log error and continue checking for other permissions
			logger.Error("error while checking permission",
				zap.String("SA_Name", sa.Name),
				zap.String("resource", permission.gvr.Resource),
				zap.String("namespace", sa.Namespace),
				zap.String("error", err.Error()))
		} else if !isPermissionExists {
			// permission not exists, create role and rolebinding accordingly
			logger.Debug("permission not exists, creating role and rolebinding",
				zap.String("SA_Name", sa.Name),
				zap.String("resource", permission.gvr.Resource),
				zap.String("namespace", sa.Namespace))
			SetupRoleAndBinding(ctx, client, logger, permission, sa)
		}
		// do nothing as permission already exists
	}
}

func SetupRoleAndBinding(ctx context.Context, client kubernetes.Interface, logger *zap.Logger, permission PermissionCheck, sa *v1.ServiceAccount) {
	roleName, err := GetRoleAndRoleBindingName(fmt.Sprintf("%s-role", sa.Name))
	if err != nil {
		logger.Error("error while generating role name",
			zap.String("SA_Name", sa.Name),
			zap.String("namespace", sa.Namespace),
			zap.String("error", err.Error()))
		return
	}

	logger.Debug("creating role",
		zap.String("role_name", roleName),
		zap.String("SA_Name", sa.Name),
		zap.String("namespace", sa.Namespace))

	roleObj := &rbac.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName,
			Namespace: sa.Namespace,
		},
		Rules: []rbac.PolicyRule{
			{
				APIGroups: []string{permission.gvr.Group},
				Resources: []string{permission.gvr.Resource},
				Verbs:     []string{permission.verb},
			},
		},
	}
	role, err := client.RbacV1().Roles(sa.Namespace).Create(ctx, roleObj, metav1.CreateOptions{})
	if err != nil {
		logger.Error("error while creating role",
			zap.String("SA_Name", sa.Name),
			zap.String("namespace", sa.Namespace),
			zap.String("error", err.Error()))
		return
	}
	logger.Info("role created successfully",
		zap.String("role_name", role.Name),
		zap.String("namespace", role.Namespace),
		zap.String("SA_Name", sa.Name))

	roleBindingName, err := GetRoleAndRoleBindingName(fmt.Sprintf("%s-rolebinding", sa.Name))
	if err != nil {
		logger.Error("error while generating rolebinding name",
			zap.String("SA_Name", sa.Name),
			zap.String("namespace", sa.Namespace),
			zap.String("error", err.Error()))
		return
	}

	logger.Debug("creating role binding",
		zap.String("rolebinding_name", roleBindingName),
		zap.String("SA_Name", sa.Name),
		zap.String("namespace", sa.Namespace))

	roleBindingObj := &rbac.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleBindingName,
			Namespace: sa.Namespace,
		},
		Subjects: []rbac.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      sa.Name,
				Namespace: sa.Namespace,
			},
		},
		RoleRef: rbac.RoleRef{
			Kind: "Role",
			Name: roleName,
		},
	}
	roleBinding, err := client.RbacV1().RoleBindings(sa.Namespace).Create(context.TODO(), roleBindingObj, metav1.CreateOptions{})
	if err != nil {
		logger.Error("error while creating rolebinding",
			zap.String("SA_Name", sa.Name),
			zap.String("namespace", sa.Namespace),
			zap.String("error", err.Error()))
		return
	}
	logger.Debug("role binding created successfully",
		zap.String("rolebinding_name", roleBinding.Name),
		zap.String("namespace", roleBinding.Namespace),
		zap.String("SA_Name", sa.Name))
}

func CheckPermission(ctx context.Context, client kubernetes.Interface, sa *v1.ServiceAccount, gvr schema.GroupVersionResource, verb string) (bool, error) {
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
		return false, err
	}

	if !r.Status.Allowed {
		return false, nil
	}
	return true, nil
}

// SetupSA create service account
func SetupSA(ctx context.Context, k8sClient kubernetes.Interface, SAName, ns string) (*v1.ServiceAccount, error) {
	saObj := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      SAName,
		},
	}
	sa, err := k8sClient.CoreV1().ServiceAccounts(ns).Create(ctx, saObj, metav1.CreateOptions{})
	return sa, err
}

// IsSAExists check whether service account exists or not in namespace
func IsSAExists(ctx context.Context, k8sClient kubernetes.Interface, sa, ns string) (*v1.ServiceAccount, error) {
	saObj, err := k8sClient.CoreV1().ServiceAccounts(ns).Get(ctx, sa, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return saObj, nil
}

// GetRoleAndRoleBindingName generate role and rolebinding name with random 6 char string as suffix
func GetRoleAndRoleBindingName(name string) (string, error) {
	id, err := uuid.NewV4()
	if err != nil {
		return "", nil
	}
	return fmt.Sprintf("%s-%s", name, id.String()[:6]), nil
}
