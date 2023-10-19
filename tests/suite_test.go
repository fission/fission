package test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fission/fission/pkg/buildermgr"
	"github.com/fission/fission/pkg/executor"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/router"
	"github.com/fission/fission/pkg/storagesvc"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"k8s.io/client-go/kubernetes"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var testEnv *envtest.Environment
var ctx context.Context
var cancel context.CancelFunc
var fissionClient *versioned.Clientset
var kubernetesClient *kubernetes.Clientset
var metricsClient *metricsclient.Clientset

func TestWorker(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Worker Fission Suite")
}

var _ = BeforeSuite(func() {
	logger := loggerfactory.GetLogger()
	ctx, cancel = context.WithCancel(context.TODO())
	defer cancel()

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "crds", "v1")},
		ErrorIfCRDPathMissing: true,
		CRDInstallOptions: envtest.CRDInstallOptions{
			MaxTime: 60 * time.Second,
		},
	}

	fmt.Println("--starting test env--")
	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	fissionClient, err = versioned.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())
	Expect(fissionClient).NotTo(BeNil())

	kubernetesClient, err = kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())
	Expect(kubernetesClient).NotTo(BeNil())

	metricsClient, err = metricsclient.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())
	Expect(metricsClient).NotTo(BeNil())

	fmt.Println("--starting executor--")
	err = executor.StartExecutor(ctx, logger, 8888, fissionClient, kubernetesClient, metricsClient)
	Expect(err).NotTo(HaveOccurred())

	fmt.Println("--starting storagesvc--")
	os.Setenv("PRUNE_ENABLED", "true")
	os.Setenv("PRUNE_INTERVAL", "60")
	_ = os.Mkdir("/tmp/test1234", os.ModePerm)
	err = storagesvc.Start(ctx, logger, storagesvc.NewLocalStorage("/tmp/test1234"), 8000)
	Expect(err).NotTo(HaveOccurred())

	fmt.Println("--starting buildermgr--")
	err = buildermgr.Start(ctx, logger, "http://storagesvc.fission", fissionClient, kubernetesClient)
	Expect(err).NotTo(HaveOccurred())

	fmt.Println("--starting router--S")
	os.Setenv("ROUTER_ROUND_TRIP_TIMEOUT", "50ms")
	os.Setenv("ROUTER_ROUNDTRIP_TIMEOUT_EXPONENT", "2")
	os.Setenv("ROUTER_ROUND_TRIP_KEEP_ALIVE_TIME", "30s")
	os.Setenv("ROUTER_ROUND_TRIP_DISABLE_KEEP_ALIVE", "true")
	os.Setenv("ROUTER_ROUND_TRIP_MAX_RETRIES", "10")
	os.Setenv("ROUTER_SVC_ADDRESS_MAX_RETRIES", "5")
	os.Setenv("ROUTER_SVC_ADDRESS_UPDATE_TIMEOUT", "30s")
	os.Setenv("ROUTER_UNTAP_SERVICE_TIMEOUT", "3600s")
	os.Setenv("USE_ENCODED_PATH", "false")
	os.Setenv("DISPLAY_ACCESS_LOG", "false")
	os.Setenv("DEBUG_ENV", "false")
	router.Start(ctx, logger, 8889, "http://executor.fission", fissionClient, kubernetesClient)
})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
