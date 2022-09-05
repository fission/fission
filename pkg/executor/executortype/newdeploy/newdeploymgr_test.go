package newdeploy

import (
	"context"
	"log"
	"testing"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	fClient "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	genInformer "github.com/fission/fission/pkg/generated/informers/externalversions"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	uuid "github.com/satori/go.uuid"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

var (
	g struct {
		cmd.CommandActioner
	}
)

func TestRefreshFuncPods(t *testing.T) {
	logger := loggerfactory.GetLogger()
	kubernetesClient := fake.NewSimpleClientset()
	fissionClient := fClient.NewSimpleClientset()
	informerFactory := genInformer.NewSharedInformerFactory(fissionClient, time.Minute*30)
	funcInformer := informerFactory.Core().V1().Functions()
	envInformer := informerFactory.Core().V1().Environments()

	newDeployInformerFactory, err := utils.GetInformerFactoryByExecutor(kubernetesClient, fv1.ExecutorTypeNewdeploy, time.Minute*30)
	if err != nil {
		t.Fatalf("Error creating informer factory: %v", err)
	}

	deployInformer := newDeployInformerFactory.Apps().V1().Deployments()
	svcInformer := newDeployInformerFactory.Core().V1().Services()
	namespace := "fission-function"

	ctx := context.Background()
	BuildConfigMap(kubernetesClient)

	podSpecPatch, err := util.GetSpecFromConfigMap(ctx, kubernetesClient, fv1.RuntimePodSpecConfigmap, namespace)
	if err != nil {
		t.Fatalf("Error creating pod spec: %v", err)
	}

	fetcherConfig, err := fetcherConfig.MakeFetcherConfig("/userfunc")
	if err != nil {
		t.Fatalf("Error creating fetcher config: %v", err)
	}

	ppc, err := MakeNewDeploy(logger, fissionClient, kubernetesClient, namespace, fetcherConfig, "test",
		funcInformer, envInformer, deployInformer, svcInformer, podSpecPatch)
	if err != nil {
		t.Fatalf("new deploy manager creation failed: %v", err)
	}

	envUID, err := uuid.NewV4()
	if err != nil {
		t.Fatal(err)
	}
	testEnv := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fission-env",
			Namespace: namespace,
			UID:       types.UID(envUID.String()),
		},
		Spec: fv1.EnvironmentSpec{
			Version: 1,
			Runtime: fv1.Runtime{
				Image: "gcr.io/xyz",
			},
			Resources: v1.ResourceRequirements{},
			Poolsize:  3,
		},
	}

	_, err2 := fissionClient.CoreV1().Environments(namespace).Create(ctx, testEnv, metav1.CreateOptions{})
	if err2 != nil {
		t.Fatalf("creating environment failed : %v", err2)
	}

	funcUID, err := uuid.NewV4()
	if err != nil {
		t.Fatal(err)
	}

	labels := map[string]string{
		"name":      "fission-env",
		"namespace": namespace,
		"uid":       string(testEnv.ObjectMeta.UID),
	}
	funcSpec := fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			UID:    types.UID(funcUID.String()),
			Labels: labels,
		},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{
				Name:      "fission-env",
				Namespace: namespace,
			},
		},
	}
	_, err3 := fissionClient.CoreV1().Functions(namespace).Create(ctx, &funcSpec, metav1.CreateOptions{})
	if err3 != nil {
		t.Fatalf("failed to create function : %v", err3)
	}

	deploySpec := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "testdeploy",
			Labels:    labels,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  testEnv.ObjectMeta.Name,
							Image: testEnv.Spec.Runtime.Image,
						},
					},
				},
			},
		},
	}

	_, err4 := kubernetesClient.AppsV1().Deployments(namespace).Create(ctx, deploySpec, metav1.CreateOptions{})
	if err4 != nil {
		t.Fatalf("failed to create deployment : %v", err4)
	}
	err5 := ppc.RefreshFuncPods(ctx, logger, funcSpec)
	if err5 != nil {
		t.Fatalf("failed to patch : %v", err5)
	}
}

func BuildConfigMap(kubernetesClient *fake.Clientset) {

	configMapData := make(map[string]string, 0)
	// 	specPatch := `
	// securityContext:
	//   fsGroup: 10001
	//   runAsGroup: 10001
	//   runAsNonRoot: true
	//   runAsUser: 10001`

	// configMapData["spec"] = specPatch

	testConfigMap := apiv1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fv1.RuntimePodSpecConfigmap,
			Namespace: "fission-function",
		},
		Data: configMapData,
	}

	configmap, err := kubernetesClient.CoreV1().ConfigMaps("fission-function").Create(context.Background(), &testConfigMap, metav1.CreateOptions{})
	if err != nil {
		log.Fatalf("Error creating configmap %v", err)
	}

	log.Printf("Configmap: %v", configmap.Data)
}
