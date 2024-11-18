package utils

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"go.uber.org/zap"
	authorizationv1 "k8s.io/api/authorization/v1"
	v1 "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/fission/fission/pkg/utils/uuid"
)

const (
	FetcherSAName   string = "fission-fetcher"
	BuilderSAName   string = "fission-builder"
	ENV_CREATE_SA   string = "SERVICEACCOUNT_CHECK_ENABLED"
	ENV_SA_INTERVAL string = "SERVICEACCOUNT_CHECK_INTERVAL"
)

type (
	ServiceAccount struct {
		kubernetesClient kubernetes.Interface
		logger           *zap.Logger
		nsResolver       *NamespaceResolver
		permissions      []*ServiceAccountPermissions
	}

	ServiceAccountPermissions struct {
		saName      string
		permissions []*PermissionCheck
	}
	PermissionCheck struct {
		gvr    *schema.GroupVersionResource
		verb   string
		exists bool
	}
)

var (
	fetcherCheck = &ServiceAccountPermissions{
		saName: FetcherSAName,
		permissions: []*PermissionCheck{
			{
				gvr:  &schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"},
				verb: "get",
			},
			{
				gvr:  &schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"},
				verb: "get",
			},
			{
				gvr:  &schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"},
				verb: "get",
			},
			{
				gvr:  &schema.GroupVersionResource{Group: "fission.io", Version: "v1", Resource: "packages"},
				verb: "get",
			},
			{
				gvr:  &schema.GroupVersionResource{Group: "", Version: "v1", Resource: "events"},
				verb: "create",
			},
		},
	}
	builderCheck = &ServiceAccountPermissions{
		saName: BuilderSAName,
		permissions: []*PermissionCheck{
			{
				gvr:  &schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"},
				verb: "get",
			},
			{
				gvr:  &schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"},
				verb: "get",
			},
			{
				gvr:  &schema.GroupVersionResource{Group: "fission.io", Version: "v1", Resource: "packages"},
				verb: "get",
			},
		},
	}
)

func CreateMissingPermissionForSA(ctx context.Context, kubernetesClient kubernetes.Interface, logger *zap.Logger) {
	enableSA := createServiceAccount()
	if enableSA {
		interval := getSAInterval()
		logger.Debug("interval value", zap.Any("interval", interval))
		sa := getSAObj(kubernetesClient, logger)
		logger.Info("Starting service account check", zap.Any("interval", interval))
		if interval > 0 {
			go wait.UntilWithContext(ctx, sa.runSACheck, interval)
		} else {
			sa.runSACheck(ctx)
		}
	}
}

func getSAObj(kubernetesClient kubernetes.Interface, logger *zap.Logger) *ServiceAccount {
	saObj := &ServiceAccount{
		kubernetesClient: kubernetesClient,
		logger:           logger,
		nsResolver:       DefaultNSResolver(),
	}
	saObj.permissions = append(saObj.permissions, fetcherCheck)
	saObj.permissions = append(saObj.permissions, builderCheck)
	return saObj
}

func (sa *ServiceAccount) runSACheck(ctx context.Context) {
	for _, ns := range sa.nsResolver.FissionResourceNS {
		for _, permission := range sa.permissions {
			if permission.saName == BuilderSAName {
				ns = sa.nsResolver.GetBuilderNS(ns)
			} else {
				ns = sa.nsResolver.GetFunctionNS(ns)
			}
			setupSAAndRoleBindings(ctx, sa.kubernetesClient, sa.logger, ns, permission)
		}
	}
}

func setupSAAndRoleBindings(ctx context.Context, client kubernetes.Interface, logger *zap.Logger, namespace string, ps *ServiceAccountPermissions) {
	SAObj, err := createGetSA(ctx, client, ps.saName, namespace)
	if err != nil {
		logger.Error("error while creating or getting service account",
			zap.String("sa_name", ps.saName),
			zap.String("namespace", namespace),
			zap.Error(err))
		return
	}

	var rules []rbac.PolicyRule

	for _, permission := range ps.permissions {
		permission.exists, err = checkPermission(ctx, client, SAObj, permission.gvr, permission.verb)
		if err != nil {
			//  some error occurred while checking permission, log error as warning message and continue to create new permissions
			logger.Info(err.Error())
		}
		if !permission.exists {
			logger.Info("creating new permission",
				zap.String("service_account", SAObj.Name),
				zap.String("namespace", SAObj.Namespace),
				zap.String("group", permission.gvr.Group),
				zap.String("resource", permission.gvr.Resource),
				zap.String("verb", permission.verb))

			rules = append(rules, rbac.PolicyRule{
				APIGroups: []string{permission.gvr.Group},
				Resources: []string{permission.gvr.Resource},
				Verbs:     []string{permission.verb},
			})
		}
	}

	if len(rules) > 0 {
		suffix, err := generateSuffix()
		if err != nil {
			logger.Error("error while generating random suffix", zap.Error(err))
		}
		// permission not exists, setup roles for the same
		role, err := setupRoles(ctx, client, logger, SAObj, rules, suffix)
		if err != nil {
			logger.Error("error while creating roles", zap.Error(err))
			return
		}
		_, err = setupRoleBinding(ctx, client, logger, SAObj, role, suffix)
		if err != nil {
			logger.Error("error while creating role bindings", zap.Error(err))
			return
		}
	}
}

func setupRoles(ctx context.Context, client kubernetes.Interface, logger *zap.Logger, sa *v1.ServiceAccount, rules []rbac.PolicyRule, suffix string) (*rbac.Role, error) {
	logger.Debug("creating role",
		zap.String("role_name", fmt.Sprintf("%s-role-%s", sa.Name, suffix)),
		zap.String("sa_name", sa.Name),
		zap.String("namespace", sa.Namespace))

	roleObj := &rbac.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-role-%s", sa.Name, suffix),
			Namespace: sa.Namespace,
		},
		Rules: rules,
	}
	role, err := client.RbacV1().Roles(sa.Namespace).Create(ctx, roleObj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("error while creating role for sa %s in namespace %s error: %s", sa.Name, sa.Namespace, err.Error())
	}
	logger.Info("role created successfully",
		zap.String("role_name", role.Name),
		zap.String("namespace", role.Namespace),
		zap.String("sa_name", sa.Name))
	return role, nil
}

func setupRoleBinding(ctx context.Context, client kubernetes.Interface, logger *zap.Logger, sa *v1.ServiceAccount, role *rbac.Role, suffix string) (*rbac.RoleBinding, error) {
	logger.Debug("creating role binding",
		zap.String("rolebinding_name", fmt.Sprintf("%s-rolebinding-%s", sa.Name, suffix)),
		zap.String("sa_name", sa.Name),
		zap.String("namespace", sa.Namespace))

	roleBindingObj := &rbac.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-rolebinding-%s", sa.Name, suffix),
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
			Name: role.Name,
		},
	}
	roleBinding, err := client.RbacV1().RoleBindings(sa.Namespace).Create(ctx, roleBindingObj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("error while creating rolebinding for sa %s in namespace %s error: %s", sa.Name, sa.Namespace, err.Error())
	}
	logger.Info("role binding created successfully",
		zap.String("rolebinding_name", roleBinding.Name),
		zap.String("namespace", roleBinding.Namespace),
		zap.String("sa_name", sa.Name))
	return roleBinding, nil
}

func checkPermission(ctx context.Context, client kubernetes.Interface, sa *v1.ServiceAccount, gvr *schema.GroupVersionResource, verb string) (bool, error) {
	user := fmt.Sprintf("system:serviceaccount:%s:%s", sa.Namespace, sa.Name)
	sar := authorizationv1.LocalSubjectAccessReview{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: sa.Namespace,
		},
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
	r, err := client.AuthorizationV1().LocalSubjectAccessReviews(sa.Namespace).Create(ctx, &sar, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("error occurred while checking permission for sa %s error: %s", user, err.Error())
	}

	if !r.Status.Allowed {
		return false, fmt.Errorf("permission %s/%s/%s-%s denied for sa %s", gvr.Group, gvr.Version, gvr.Resource, verb, user)
	}
	return true, nil
}

// CreateGetSA => create service account if not exists else get it.
func createGetSA(ctx context.Context, k8sClient kubernetes.Interface, SAName, ns string) (*v1.ServiceAccount, error) {
	saObj, err := k8sClient.CoreV1().ServiceAccounts(ns).Get(ctx, SAName, metav1.GetOptions{})
	if err != nil && k8serrors.IsNotFound(err) {
		saObj = &v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
				Name:      SAName,
			},
		}
		saObj, err = k8sClient.CoreV1().ServiceAccounts(ns).Create(ctx, saObj, metav1.CreateOptions{})
		if err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	return saObj, nil
}

// generateSuffix generates a random string of 6 characters
func generateSuffix() (string, error) {
	id := uuid.NewString()
	return id[:6], nil
}

func createServiceAccount() bool {
	createSA, err := strconv.ParseBool(os.Getenv(ENV_CREATE_SA))
	if err != nil {
		return false
	}
	return createSA
}

func getSAInterval() time.Duration {
	SAInterval, _ := GetUIntValueFromEnv(ENV_SA_INTERVAL)
	return time.Duration(SAInterval) * time.Minute
}
