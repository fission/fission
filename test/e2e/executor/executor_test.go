package executor_test

import (
	"context"
	"testing"
	"time"

	"github.com/fission/fission/pkg/executor"
	"github.com/fission/fission/pkg/leaderelection"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"github.com/fission/fission/pkg/utils/manager"
	"github.com/fission/fission/test/e2e/framework"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestExecutorLeaderElection(t *testing.T) {
	mgr := manager.New()
	f := framework.NewFramework()
	ctx, cancel := context.WithCancel(context.Background())
	err := f.Start(ctx)
	require.NoError(t, err)
	defer func() {
		cancel()
		mgr.Wait()
		err = f.Stop()
		require.NoError(t, err)
	}()

	k8sClient, err := f.ClientGen().GetKubernetesClient()
	require.NoError(t, err)

	executorPort, err := utils.FindFreePort()
	if err != nil {
		t.Errorf("error finding unused port: %v", err)
	}

	logger := loggerfactory.GetLogger()
	defer logger.Sync()

	t.Log("Start first executor instance")
	go func() {
		err := leaderelection.RunLeaderElection(ctx, logger, "executor", f.ClientGen(), func() error {
			return executor.StartExecutor(ctx, f.ClientGen(), logger, mgr, executorPort)
		})
		if err != nil {
			logger.Error("Failed to start the first executor instance", zap.Error(err))
		}
	}()

	t.Log("Start second executor instance")
	go func() {
		err := leaderelection.RunLeaderElection(ctx, logger, "executor", f.ClientGen(), func() error {
			return executor.StartExecutor(ctx, f.ClientGen(), logger, mgr, executorPort)
		})
		if err != nil {
			logger.Error("Failed to start the second executor instance", zap.Error(err))
		}
	}()

	time.Sleep(5 * time.Second)

	leases, err := k8sClient.CoordinationV1().Leases(metav1.NamespaceDefault).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to list leases: %v", err)
	}

	numLeases := len(leases.Items)
	expectedLeases := 1
	if numLeases != expectedLeases {
		t.Fatalf("expected %d leases, got %d: %v", expectedLeases, numLeases, err)
	}
}
