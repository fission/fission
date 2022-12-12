package utils

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/pkg/errors"
	uuid "github.com/satori/go.uuid"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	v1 "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const (
	FetcherSAName          string = "fission-fetcher"
	BuilderSAName          string = "fission-builder"
	ENV_CREATE_SA          string = "SERVICEACCOUNT_CHECK_ENABLED"
	ENV_SA_INTERVAL        string = "SERVICEACCOUNT_CHECK_INTERVAL"
	LABEL_ENV_NAMESPACE    string = "envNamespace"
	LABEL_DEPLOYMENT_OWNER string = "owner"
	BUILDER_MGR            string = "buildermgr"
)

type PermissionCheck struct {
	gvr    *schema.GroupVersionResource
	verb   string
	exists bool
}

type ServiceAccount struct {
	KubernetesClient kubernetes.Interface
	Logger           *zap.Logger
	SAName           string
	NSResolver       *NamespaceResolver
}

type ServiceAccountPermissionCheck struct {
	sa          string
	permissions []*PermissionCheck
}

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

func (sa *ServiceAccount) DoBuilderCheck(ctx context.Context, interval time.Duration) {
	wait.UntilWithContext(ctx, sa.checkBuilder, interval)
}

func (sa *ServiceAccount) DoFetcherCheck(ctx context.Context, interval time.Duration) {
	wait.UntilWithContext(ctx, sa.checkFetcher, interval)
}

func (sa *ServiceAccount) checkBuilder(ctx context.Context) {
	selector := map[string]string{LABEL_DEPLOYMENT_OWNER: BUILDER_MGR}
	for _, ns := range sa.NSResolver.FissionResourceNS {
		namespace := sa.NSResolver.GetBuilderNS(ns)
		selector[LABEL_ENV_NAMESPACE] = namespace
		deployList, err := sa.getBuilderDeploymentList(ctx, selector, namespace)
		if err != nil {
			sa.Logger.Error("error while getting builder deployment",
				zap.String("namespace", namespace),
				zap.Error(err))
			continue
		}
		if len(deployList) == 0 {
			sa.Logger.Info("no builder deployment found", zap.String("namspace", namespace))
		} else if len(deployList) > 1 {
			sa.Logger.Info("found more than one builder deployment", zap.String("namspace", namespace))
		} else {
			SetupSAAndRoleBindings(ctx, sa.KubernetesClient, sa.Logger, sa.SAName, namespace)
		}
	}
}

func (sa *ServiceAccount) checkFetcher(ctx context.Context) {
	for _, ns := range sa.NSResolver.FissionResourceNS {
		namespace := sa.NSResolver.GetFunctionNS(ns)
		SetupSAAndRoleBindings(ctx, sa.KubernetesClient, sa.Logger, sa.SAName, namespace)
	}
}

func (sa *ServiceAccount) getBuilderDeploymentList(ctx context.Context, sel map[string]string, ns string) ([]appsv1.Deployment, error) {
	deployList, err := sa.KubernetesClient.AppsV1().Deployments(ns).List(
		ctx,
		metav1.ListOptions{
			LabelSelector: labels.Set(sel).AsSelector().String(),
		})
	if err != nil {
		return nil, errors.Wrap(err, "error getting builder deployment list")
	}
	return deployList.Items, nil
}

func SetupSAAndRoleBindings(ctx context.Context, client kubernetes.Interface, logger *zap.Logger, SAName string, namespace string) {
	SAObj, err := CreateGetSA(ctx, client, SAName, namespace)
	if err != nil {
		logger.Error("error while creating or getting service account",
			zap.String("SA_name", SAName),
			zap.String("namespace", namespace))
		return
	}

	var permissions []*PermissionCheck
	var rules []rbac.PolicyRule
	if SAName == BuilderSAName {
		permissions = builderCheck.permissions
	} else {
		permissions = fetcherCheck.permissions
	}

	for _, permission := range permissions {
		permission.exists, err = CheckPermission(ctx, client, SAObj, permission.gvr, permission.verb)
		if err != nil {
			//  some error occurred while checking permission
			//  now assume permission not exists and will add this permission in rules, insted of return
			logger.Error("error while checking permission",
				zap.String("SA_name", SAObj.Name),
				zap.String("namespace", SAObj.Namespace))
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
		role, err := SetupRoles(ctx, client, logger, SAObj, rules)
		if err != nil {
			return
		}
		SetupRoleBinding(ctx, client, logger, SAObj, role)
	}
}

func SetupRoles(ctx context.Context, client kubernetes.Interface, logger *zap.Logger, sa *v1.ServiceAccount, rules []rbac.PolicyRule) (*rbac.Role, error) {
	roleName, err := GetRoleAndRoleBindingName(fmt.Sprintf("%s-role", sa.Name))
	if err != nil {
		logger.Error("error while generating role name",
			zap.String("SA_Name", sa.Name),
			zap.String("namespace", sa.Namespace),
			zap.String("error", err.Error()))
		return nil, err
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
		logger.Error("error while creating role",
			zap.String("SA_Name", sa.Name),
			zap.String("namespace", sa.Namespace),
			zap.String("error", err.Error()))
		return nil, err
	}
	logger.Debug("role created successfully",
		zap.String("role_name", role.Name),
		zap.String("namespace", role.Namespace),
		zap.String("SA_Name", sa.Name))
	return role, nil
}

func SetupRoleBinding(ctx context.Context, client kubernetes.Interface, logger *zap.Logger, sa *v1.ServiceAccount, role *rbac.Role) {
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
			Name: role.Name,
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

func CheckPermission(ctx context.Context, client kubernetes.Interface, sa *v1.ServiceAccount, gvr *schema.GroupVersionResource, verb string) (bool, error) {
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

// CreateGetSA => create service account if not exists else get it.
func CreateGetSA(ctx context.Context, k8sClient kubernetes.Interface, SAName, ns string) (*v1.ServiceAccount, error) {
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
func GetRoleAndRoleBindingName(name string) (string, error) {
	id, err := uuid.NewV4()
	if err != nil {
		return "", nil
	}
	return fmt.Sprintf("%s-%s", name, id.String()[:6]), nil
}

func CreateServiceAccount() bool {
	if createSA, err := strconv.ParseBool(os.Getenv(ENV_CREATE_SA)); err != nil {
		return createSA
	}
	return false
}

func GetSAInterval() time.Duration {
	SAInterval, err := GetUIntValueFromEnv(ENV_SA_INTERVAL)
	if err != nil {
		return time.Duration(30) * time.Minute
	}
	return time.Duration(SAInterval) * time.Minute
}

func GetSAObj(k8sclient kubernetes.Interface, logger *zap.Logger, saName string) *ServiceAccount {
	return &ServiceAccount{
		SAName:           saName,
		KubernetesClient: k8sclient,
		Logger:           logger,
		NSResolver:       DefaultNSResolver(),
	}
}
