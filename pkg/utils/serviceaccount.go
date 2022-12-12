package utils

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	uuid "github.com/satori/go.uuid"

	"go.uber.org/zap"
	authorizationv1 "k8s.io/api/authorization/v1"
	v1 "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const (
	FetcherSAName   string = "fission-fetcher"
	BuilderSAName   string = "fission-builder"
	ENV_CREATE_SA   string = "SERVICEACCOUNT_CHECK_ENABLED"
	ENV_SA_INTERVAL string = "SERVICEACCOUNT_CHECK_INTERVAL"
)

type (
	PermissionCheck struct {
		gvr    *schema.GroupVersionResource
		verb   string
		exists bool
	}

	ServiceAccount struct {
		kubernetesClient kubernetes.Interface
		logger           *zap.Logger
		nsResolver       *NamespaceResolver
	}

	ServiceAccountPermissionCheck struct {
		sa          string
		permissions []*PermissionCheck
	}
)

var (
	fetcherCheck = &ServiceAccountPermissionCheck{
		sa: FetcherSAName,
		permissions: []*PermissionCheck{
			{
				gvr:  &schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"},
				verb: "list",
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
				gvr:  &schema.GroupVersionResource{Group: "fission.io", Version: "v1", Resource: "events"},
				verb: "create",
			},
		},
	}
	builderCheck = &ServiceAccountPermissionCheck{
		sa: BuilderSAName,
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
		sa := &ServiceAccount{
			kubernetesClient: kubernetesClient,
			logger:           logger,
			nsResolver:       DefaultNSResolver(),
		}
		go sa.doSAFetcherCheck(ctx, interval)
	}
}

func (sa *ServiceAccount) doSAFetcherCheck(ctx context.Context, interval time.Duration) {
	wait.UntilWithContext(ctx, sa.runSAFetcherCheck, interval)
}

func (sa *ServiceAccount) runSAFetcherCheck(ctx context.Context) {
	for _, ns := range sa.nsResolver.FissionResourceNS {
		setupSAAndRoleBindings(ctx, sa.kubernetesClient, sa.logger, BuilderSAName, sa.nsResolver.GetBuilderNS(ns), builderCheck)
	}
	for _, ns := range nsResolver.FissionResourceNS {
		setupSAAndRoleBindings(ctx, sa.kubernetesClient, sa.logger, FetcherSAName, sa.nsResolver.GetFunctionNS(ns), fetcherCheck)
	}
}

func setupSAAndRoleBindings(ctx context.Context, client kubernetes.Interface, logger *zap.Logger, SAName string, namespace string, pc *ServiceAccountPermissionCheck) {
	SAObj, err := createGetSA(ctx, client, SAName, namespace)
	if err != nil {
		logger.Error("error while creating or getting service account",
			zap.String("SA_name", SAName),
			zap.String("namespace", namespace),
			zap.Error(err))
		return
	}

	var rules []rbac.PolicyRule

	for _, permission := range pc.permissions {
		permission.exists, err = checkPermission(ctx, client, SAObj, permission.gvr, permission.verb)
		if err != nil {
			//  some error occurred while checking permission
			//  now assume permission not exists and will add this permission in rules, insted of return
			logger.Error("error while checking permission", zap.Error(err))
		}
		if !permission.exists {
			rules = append(rules, rbac.PolicyRule{
				APIGroups: []string{permission.gvr.Group},
				Resources: []string{permission.gvr.Resource},
				Verbs:     []string{permission.verb},
			})
		}
	}

	if len(rules) > 0 {
		// permission not exists, setup roles for the same
		role, err := setupRoles(ctx, client, logger, SAObj, rules)
		if err != nil {
			logger.Error("error while creating roles", zap.Error(err))
			return
		}
		_, err = setupRoleBinding(ctx, client, logger, SAObj, role)
		if err != nil {
			logger.Error("error while creating role bindings", zap.Error(err))
			return
		}
	}
}

func setupRoles(ctx context.Context, client kubernetes.Interface, logger *zap.Logger, sa *v1.ServiceAccount, rules []rbac.PolicyRule) (*rbac.Role, error) {
	roleName, err := getRoleAndRoleBindingName(fmt.Sprintf("%s-role", sa.Name))
	if err != nil {
		return nil, fmt.Errorf("error while generating role name for sa %s in namespace %s error: %s", sa.Name, sa.Namespace, err.Error())
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
		Rules: rules,
	}
	role, err := client.RbacV1().Roles(sa.Namespace).Create(ctx, roleObj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("error while creating role for sa %s in namespace %s error: %s", sa.Name, sa.Namespace, err.Error())
	}
	logger.Debug("role created successfully",
		zap.String("role_name", role.Name),
		zap.String("namespace", role.Namespace),
		zap.String("SA_Name", sa.Name))
	return role, nil
}

func setupRoleBinding(ctx context.Context, client kubernetes.Interface, logger *zap.Logger, sa *v1.ServiceAccount, role *rbac.Role) (*rbac.RoleBinding, error) {
	roleBindingName, err := getRoleAndRoleBindingName(fmt.Sprintf("%s-rolebinding", sa.Name))
	if err != nil {
		return nil, fmt.Errorf("error while generating rolebinding name for sa %s in namespace %s error: %s", sa.Name, sa.Namespace, err.Error())
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
			Name: role.Name,
		},
	}
	roleBinding, err := client.RbacV1().RoleBindings(sa.Namespace).Create(context.TODO(), roleBindingObj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("error while creating rolebinding for sa %s in namespace %s error: %s", sa.Name, sa.Namespace, err.Error())
	}
	logger.Debug("role binding created successfully",
		zap.String("rolebinding_name", roleBinding.Name),
		zap.String("namespace", roleBinding.Namespace),
		zap.String("SA_Name", sa.Name))
	return roleBinding, nil
}

func checkPermission(ctx context.Context, client kubernetes.Interface, sa *v1.ServiceAccount, gvr *schema.GroupVersionResource, verb string) (bool, error) {
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

// GetRoleAndRoleBindingName generate role and rolebinding name with random 6 char string as suffix
func getRoleAndRoleBindingName(name string) (string, error) {
	id, err := uuid.NewV4()
	if err != nil {
		return "", nil
	}
	return fmt.Sprintf("%s-%s", name, id.String()[:6]), nil
}

func createServiceAccount() bool {
	createSA, err := strconv.ParseBool(os.Getenv(ENV_CREATE_SA))
	if err != nil {
		return false
	}
	return createSA
}

func getSAInterval() time.Duration {
	SAInterval, err := GetUIntValueFromEnv(ENV_SA_INTERVAL)
	if err != nil {
		return time.Duration(30) * time.Minute
	}
	return time.Duration(SAInterval) * time.Minute
}
