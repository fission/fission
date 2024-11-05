package leaderelection

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/dchest/uniuri"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

type LeaderElection interface {
	// Start the leader election code loop
	RunLeaderElection(ctx context.Context, logger *zap.Logger, lock *resourcelock.LeaseLock, function func() error)
}

// CreateLeaseLockObject will fetch K8s config using InClusterConfig
// and will create a `Lease` K8s object using pod_name and pod_namespace
func createLeaseLockObject(leaseLockName string, clientGen crd.ClientGeneratorInterface) (*resourcelock.LeaseLock, error) {
	identity := os.Getenv("HOSTNAME")
	if identity == "" {
		identity = uniuri.NewLen(6)
	}

	client, err := clientGen.GetKubernetesClient()
	if err != nil {
		return nil, errors.Wrapf(err, "failed loading K8s client")
	}

	leaseLockNamespace := utils.GetInClusterConfigNamespace()
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      leaseLockName,
			Namespace: leaseLockNamespace,
		},
		Client: client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: identity,
		},
	}

	return lock, nil
}

func RunLeaderElection(ctx context.Context, logger *zap.Logger, leaseLockName string, clientGen crd.ClientGeneratorInterface, f func() error) error {
	logger.Info("starting leader election")
	lock, err := createLeaseLockObject(leaseLockName, clientGen)
	if err != nil {
		return err
	}

	identity := lock.LockConfig.Identity
	leConfig := leaderelection.LeaderElectionConfig{
		Lock: lock,
		// IMPORTANT: you MUST ensure that any code you have that
		// is protected by the lease must terminate **before**
		// you call cancel. Otherwise, you could have a background
		// loop still running and another process could
		// get elected before your background loop finished, violating
		// the stated goal of the lease.
		ReleaseOnCancel: true,
		LeaseDuration:   10 * time.Second,
		RenewDeadline:   5 * time.Second,
		RetryPeriod:     1 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				logger.Info("started leading", zap.String("identity", identity))
				err := f()
				if err != nil {
					logger.Error("error executing function", zap.Error(err))
				}
			},
			OnStoppedLeading: func() {
				logger.Info("leader lost", zap.String("identity", identity))
			},
			OnNewLeader: func(leaderIdentity string) {
				logger.Info("new leader elected", zap.String("leaderIdentity", leaderIdentity))
				if leaderIdentity == identity {
					return
				}
				logger.Info("waiting for leader election", zap.String("identity", identity))
			},
		},
	}

	ctxle, cancel := context.WithCancel(ctx)
	defer cancel()

	wg := &sync.WaitGroup{}
	wg.Add(1)

	go func() {
		defer wg.Done()
		leaderelection.RunOrDie(ctxle, leConfig)
	}()

	wg.Wait()
	cancel()

	logger.Info("stopped leader election loop")
	return nil
}
