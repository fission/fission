package newdeploy

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/fake"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	fClient "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	genInformer "github.com/fission/fission/pkg/generated/informers/externalversions"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"github.com/fission/fission/pkg/utils/manager"
	"github.com/fission/fission/pkg/utils/uuid"
)

const (
	defaultNamespace  string = "default"
	functionNamespace string = "fission-function"
	builderNamespace  string = "fission-builder"
	envName           string = "newdeploy-test-env"
	functionName      string = "newdeploy-test-func"
	configmapName     string = "newdeploy-test-configmap"
)

func TestRefreshFuncPods(t *testing.T) {
	os.Setenv("DEBUG_ENV", "true")
	mgr := manager.New()
	defer mgr.Wait()
	logger := loggerfactory.GetLogger()
	kubernetesClient := fake.NewSimpleClientset()
	fissionClient := fClient.NewSimpleClientset()
	factory := make(map[string]genInformer.SharedInformerFactory, 0)
	factory[metav1.NamespaceAll] = genInformer.NewSharedInformerFactory(fissionClient, time.Minute*30)

	executorLabel, err := utils.GetInformerLabelByExecutor(fv1.ExecutorTypeNewdeploy)
	if err != nil {
		t.Fatalf("Error creating labels for informer: %s", err)
	}
	ndmInformerFactory := utils.GetInformerFactoryByExecutor(kubernetesClient, executorLabel, time.Minute*30)

	ctx := t.Context()

	fetcherConfig, err := fetcherConfig.MakeFetcherConfig("/userfunc")
	if err != nil {
		t.Fatalf("Error creating fetcher config: %s", err)
	}

	executor, err := MakeNewDeploy(ctx, logger, fissionClient, kubernetesClient, fetcherConfig, "test",
		factory, ndmInformerFactory, nil)
	if err != nil {
		t.Fatalf("new deploy manager creation failed: %s", err)
	}

	ndm := executor.(*NewDeploy)

	nsResolver := utils.NamespaceResolver{
		FunctionNamespace: functionNamespace,
		BuilderNamespace:  builderNamespace,
		DefaultNamespace:  defaultNamespace,
	}
	ndm.nsResolver = &nsResolver

	mgr.Add(ctx, func(ctx context.Context) {
		ndm.Run(ctx, mgr)
	})
	t.Log("New deploy manager started")

	for _, f := range factory {
		f.Start(ctx.Done())
	}
	for _, informerFactory := range ndmInformerFactory {
		informerFactory.Start(ctx.Done())
	}

	t.Log("Informers required for new deploy manager started")

	waitSynced := make([]k8sCache.InformerSynced, 0)
	for _, deplListerSynced := range ndm.deplListerSynced {
		waitSynced = append(waitSynced, deplListerSynced)
	}
	for _, svcListerSynced := range ndm.svcListerSynced {
		waitSynced = append(waitSynced, svcListerSynced)
	}

	if ok := k8sCache.WaitForCacheSync(ctx.Done(), waitSynced...); !ok {
		t.Fatal("Timed out waiting for caches to sync")
	}

	envSpec := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envName,
			Namespace: defaultNamespace,
			UID:       "83c82da2-81e9-4ebd-867e-f383e65e603f",
		},
		Spec: fv1.EnvironmentSpec{
			Version: 1,
			Runtime: fv1.Runtime{
				Image: "gcr.io/xyz",
			},
		},
	}

	_, err = fissionClient.CoreV1().Environments(defaultNamespace).Create(ctx, envSpec, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating environment failed : %s", err)
	}

	envRes, err := fissionClient.CoreV1().Environments(defaultNamespace).Get(ctx, envName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Error getting environment: %s", err)
	}
	assert.Equal(t, envRes.ObjectMeta.Name, envName)

	funcSpec := fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:      functionName,
			Namespace: defaultNamespace,
			UID:       uuid.NewUUID(),
		},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{
				Name:      envName,
				Namespace: defaultNamespace,
			},
			InvokeStrategy: fv1.InvokeStrategy{
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType: fv1.ExecutorTypeNewdeploy,
				},
			},
		},
	}
	_, err = fissionClient.CoreV1().Functions(defaultNamespace).Create(ctx, &funcSpec, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating function failed : %s", err)
	}

	funcRes, err := fissionClient.CoreV1().Functions(defaultNamespace).Get(ctx, functionName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Error getting function: %s", err)
	}
	assert.Equal(t, funcRes.ObjectMeta.Name, functionName)

	ctx2, cancel2 := context.WithCancel(context.Background())
	wait.Until(func() {
		t.Log("Checking for deployment")
		ret, err := kubernetesClient.AppsV1().Deployments(functionNamespace).List(ctx2, metav1.ListOptions{})
		if err != nil {
			t.Fatalf("Error getting deployment: %s", err)
		}
		if len(ret.Items) > 0 {
			t.Log("Deployment created", ret.Items[0].Name)
			cancel2()
		}
	}, time.Second*2, ctx2.Done())

	err = BuildConfigMap(ctx, kubernetesClient, defaultNamespace, configmapName, map[string]string{
		"test-key": "test-value",
	})
	if err != nil {
		t.Fatalf("Error building configmap: %s", err)
	}

	t.Log("Adding configmap to function")
	funcRes.Spec.ConfigMaps = []fv1.ConfigMapReference{
		{
			Name:      configmapName,
			Namespace: defaultNamespace,
		},
	}
	_, err = fissionClient.CoreV1().Functions(defaultNamespace).Update(ctx, funcRes, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Error updating function: %s", err)
	}
	funcRes, err = fissionClient.CoreV1().Functions(defaultNamespace).Get(ctx, functionName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Error getting function: %s", err)
	}
	assert.Greater(t, len(funcRes.Spec.ConfigMaps), 0)

	err = ndm.RefreshFuncPods(ctx, logger, *funcRes)
	if err != nil {
		t.Fatalf("Error refreshing function pods: %s", err)
	}

	funcLabels := ndm.getDeployLabels(funcRes.ObjectMeta, envRes.ObjectMeta)

	dep, err := kubernetesClient.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(funcLabels).AsSelector().String(),
	})

	if err != nil {
		t.Fatalf("Error getting deployment: %s", err)
	}
	assert.Equal(t, len(dep.Items), 1)

	cm, err := kubernetesClient.CoreV1().ConfigMaps(defaultNamespace).Get(ctx, configmapName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Error getting configmap: %s", err)
	}
	assert.Equal(t, cm.ObjectMeta.Name, configmapName)
	updatedDepl := dep.Items[0]
	resourceVersionMatch := false
	assert.Equal(t, len(updatedDepl.Spec.Template.Spec.Containers), 2)
	for _, v := range updatedDepl.Spec.Template.Spec.Containers {
		if v.Name == envName {
			assert.Greater(t, len(v.Env), 0)
			for _, env := range v.Env {
				if env.Name == fv1.ResourceVersionCount {
					assert.Equal(t, env.Value, cm.ObjectMeta.ResourceVersion)
					resourceVersionMatch = true
				}
			}
		}
	}
	assert.True(t, resourceVersionMatch)
}

func FakeResourceVersion() string {
	return fmt.Sprint(time.Now().Nanosecond())[:6]
}

func BuildConfigMap(ctx context.Context, kubernetesClient *fake.Clientset, namespace, name string, data map[string]string) error {
	testConfigMap := apiv1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       namespace,
			ResourceVersion: FakeResourceVersion(),
		},
		Data: data,
	}
	_, err := kubernetesClient.CoreV1().ConfigMaps(namespace).Create(ctx, &testConfigMap, metav1.CreateOptions{})
	return err
}
