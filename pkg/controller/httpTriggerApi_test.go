package controller

import (
	"context"
	"testing"

	corev1 "github.com/fission/fission/pkg/apis/core/v1"
	fClient "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckHTTPTriggerDuplicates(t *testing.T) {
	logger := loggerfactory.GetLogger()
	kubernetesClient := fake.NewSimpleClientset()
	fissionClient := fClient.NewSimpleClientset()
	api := &API{
		logger:           logger,
		fissionClient:    fissionClient,
		kubernetesClient: kubernetesClient,
	}
	ctx := context.Background()
	prefix := "url"
	trigger, err := fissionClient.CoreV1().HTTPTriggers("fission-function").Create(ctx, &corev1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "fission-function",
		},
		Spec: corev1.HTTPTriggerSpec{
			Host:   "test.com",
			Prefix: &prefix,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		logger.Info("error found: ", zap.Error(err))
	}
	err = api.checkHTTPTriggerDuplicates(context.TODO(), trigger)
	if err != nil {
		t.Errorf("error: %s", err)
	}

	//create another trigger with different prefix
	prefix = "another_url"
	trigger, err = fissionClient.CoreV1().HTTPTriggers("fission-function").Create(ctx, &corev1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "fission-function",
		},
		Spec: corev1.HTTPTriggerSpec{
			Host:   "test.com",
			Prefix: &prefix,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		logger.Info("error found: ", zap.Error(err))
	}
	err = api.checkHTTPTriggerDuplicates(context.TODO(), trigger)
	if err != nil {
		t.Errorf("error: %s", err)
	}

}
