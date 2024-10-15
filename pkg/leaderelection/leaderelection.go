package leaderelection

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

const inClusterNamespacePath = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

type LeaderElection interface {
	// CreateLeaseLockObject will fetch K8s config using InClusterConfig
	// and will create a `Lease` K8s object using pod_name, pod_namespace and unique id
	CreateLeaseLockObject(ctx context.Context, leaseLockName, id string) (*resourcelock.LeaseLock, error)

	// Start the leader election code loop
	RunLeaderElection(ctx context.Context, logger *zap.Logger, lock *resourcelock.LeaseLock, function func() error)
}

func CreateLeaseLockObject(ctx context.Context, leaseLockName, id string) (*resourcelock.LeaseLock, error) {

	client, err := getK8sClient()
	if err != nil {
		return nil, errors.Wrapf(err, "failed loading K8s client")
	}

	leaseLockNamespace, err := getInClusterNamespace()
	if err != nil {
		return nil, errors.Wrapf(err, "unable to find leader election namespace: %v", err)
	}

	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      leaseLockName,
			Namespace: leaseLockNamespace,
		},
		Client: client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: id,
		},
	}

	return lock, nil
}

func RunLeaderElection(ctx context.Context, logger *zap.Logger, lock *resourcelock.LeaseLock, f func() error) {
	identity := lock.LockConfig.Identity
	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock: lock,
		// IMPORTANT: you MUST ensure that any code you have that
		// is protected by the lease must terminate **before**
		// you call cancel. Otherwise, you could have a background
		// loop still running and another process could
		// get elected before your background loop finished, violating
		// the stated goal of the lease.
		ReleaseOnCancel: true,
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
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
				os.Exit(0)
			},
			OnNewLeader: func(leaderIdentity string) {
				logger.Info("new leader elected", zap.String("leaderIdentity", leaderIdentity))
				if leaderIdentity == identity {
					return
				}
				logger.Info("waiting for leader election", zap.String("identity", identity))
				<-ctx.Done()
			},
		},
	})
}

func getK8sClient() (*kubernetes.Clientset, error) {
	// Use in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, errors.Wrap(err, "failed loading InClusterConfig")
	}

	// Create a clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, errors.Wrap(err, "failed creating clientset")
	}

	return clientset, nil
}

func getInClusterNamespace() (string, error) {
	// Check whether the namespace file exists.
	// If not, we are not running in cluster so can't guess the namespace.
	if _, err := os.Stat(inClusterNamespacePath); os.IsNotExist(err) {
		return "", fmt.Errorf("not running in-cluster, please specify LeaderElectionNamespace")
	} else if err != nil {
		return "", fmt.Errorf("error checking namespace file: %w", err)
	}

	// Load the namespace file and return its content
	namespace, err := os.ReadFile(inClusterNamespacePath)
	if err != nil {
		return "", fmt.Errorf("error reading namespace file: %w", err)
	}
	return string(namespace), nil
}
