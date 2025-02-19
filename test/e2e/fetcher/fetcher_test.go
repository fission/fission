package fetcher

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fission/fission/cmd/fetcher/app"
	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fetcher"
	"github.com/fission/fission/pkg/fetcher/client"
	storageClient "github.com/fission/fission/pkg/storagesvc/client"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.uber.org/zap"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/httpserver"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"github.com/fission/fission/pkg/utils/manager"
	"github.com/fission/fission/pkg/utils/profile"
	"github.com/fission/fission/test/e2e/framework"
	"github.com/fission/fission/test/e2e/framework/cli"
	"github.com/fission/fission/test/e2e/framework/services"
)

const testFileData = `
module.exports = async function (context) {
    return {
        status: 200,
        body: "Hello, Fission!\n"
    };
}
`

type FetcherTestSuite struct {
	suite.Suite
	mgr           manager.Interface
	logger        *zap.Logger
	cancel        context.CancelFunc
	framework     *framework.Framework
	ctx           context.Context
	cfgMapsDir    string
	secretsDir    string
	sharedVolDir  string
	podInfoDir    string
	envName       string
	fetcherClient client.ClientInterface
	fissionClient versioned.Interface
	k8sClient     kubernetes.Interface
	storagesvcURL string
	specTestData  *specializeTestData
}

type specializeTestData struct {
	throwError bool
}

func (f *FetcherTestSuite) SetupSuite() {

	var err error
	f.envName = "test-pythondeploy"
	const testEnvImage = "fission/python-env:latest"
	const testBuilderImage = "fission/python-builder:latest"

	f.logger = loggerfactory.GetLogger()
	f.framework = framework.NewFramework()
	f.mgr = manager.New()
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.ctx = ctx
	f.specTestData = &specializeTestData{}

	// create a dummy server to test FETCH_URL source type and specialize handler
	mux := http.NewServeMux()
	mux.HandleFunc("/specialize", func(w http.ResponseWriter, r *http.Request) {

		if f.specTestData.throwError {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v2/specialize", func(w http.ResponseWriter, r *http.Request) {

		if f.specTestData.throwError {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/files/", http.StripPrefix("/files/", http.FileServer(http.Dir("."))))

	f.mgr.Add(ctx, func(c context.Context) {
		httpserver.StartServer(ctx, f.logger, f.mgr, "specialize", "8888", mux)
	})

	err = f.framework.Start(ctx)
	require.NoError(f.T(), err)

	err = services.StartServices(ctx, f.framework, f.mgr)
	require.NoError(f.T(), err)

	err = wait.PollUntilContextTimeout(ctx, time.Second*5, time.Second*50, true, func(_ context.Context) (bool, error) {
		if err := f.framework.CheckService("webhook"); err != nil {
			return false, nil
		}
		return true, nil
	})
	require.NoError(f.T(), err)

	defer func() {
		if err != nil {
			f.TearDownSuite()
		}
	}()

	err = f.createFetcherTestDirs()
	require.NoError(f.T(), err, "error creating fetcher test dirs")

	os.Args = append(os.Args, "-secret-dir", f.secretsDir, "-cfgmap-dir", f.cfgMapsDir)
	os.Args = append(os.Args, f.sharedVolDir)

	err = f.setupPodInfoMountDir()
	require.NoError(f.T(), err)

	freePort, err := utils.FindFreePort()
	require.NoError(f.T(), err)

	fetcherPort := strconv.Itoa(freePort)

	fetcherUrl := "http://localhost:" + fetcherPort + "/"

	profile.ProfileIfEnabled(ctx, f.logger, f.mgr)
	f.mgr.Add(ctx, func(c context.Context) {
		app.Run(ctx, f.framework.ClientGen(), f.logger, f.mgr, fetcherPort, f.podInfoDir)
	})

	f.fetcherClient = client.MakeClient(f.logger, fetcherUrl)

	_, err = cli.ExecCommand(f.framework, ctx, "env", "create", "--name", f.envName, "--image", testEnvImage,
		"--builder", testBuilderImage)
	require.NoError(f.T(), err, "error in creating test environment:%s", f.envName)

	f.fissionClient, err = f.framework.ClientGen().GetFissionClient()
	require.NoError(f.T(), err)

	f.k8sClient, err = f.framework.ClientGen().GetKubernetesClient()
	require.NoError(f.T(), err)

	err = wait.PollUntilContextTimeout(f.ctx, 2*time.Second, 5*time.Minute, true, func(c context.Context) (done bool, err error) {

		deploys, err := f.k8sClient.AppsV1().Deployments(metav1.NamespaceDefault).List(c, metav1.ListOptions{})
		if err != nil || len(deploys.Items) == 0 {
			return false, err
		}

		for _, deploy := range deploys.Items {
			if strings.Contains(deploy.Name, f.envName) && (deploy.Status.AvailableReplicas == deploy.Status.Replicas) {
				return true, nil
			}
		}

		return false, nil
	})

	f.storagesvcURL, err = f.framework.GetServiceURL("storagesvc")
	require.NoError(f.T(), err, "error getting storage service URL: %w", err)

}

func (f *FetcherTestSuite) TestFetcherDeploymentType() {

	const testDeployPkg = "test-deploy-pkg"
	const testDeployArchiveFile = "data/test-deploy-pkg.zip"
	const testDeployFuncName = "deploypy"

	_, err := cli.ExecCommand(f.framework, f.ctx, "package", "create", "--name", testDeployPkg, "--deployarchive", testDeployArchiveFile, "--env", f.envName)
	require.NoError(f.T(), err, "error in creating package:%s in environment:%s", testDeployPkg, f.envName)
	err = wait.PollUntilContextTimeout(f.ctx, 10*time.Second, 5*time.Minute, true, func(c context.Context) (done bool, err error) {

		pkgs, err := f.fissionClient.CoreV1().Packages(metav1.NamespaceDefault).List(c, metav1.ListOptions{})

		if err != nil {
			return false, err
		}

		for _, pkg := range pkgs.Items {
			if pkg.Name == testDeployPkg && pkg.Status.BuildStatus == fv1.BuildStatusSucceeded {
				return true, nil
			} else if pkg.Name == testDeployPkg && pkg.Status.BuildStatus == fv1.BuildStatusFailed {
				return true, fmt.Errorf("build failed for package:%s", pkg.Name)
			}
		}

		return false, nil
	})
	require.NoError(f.T(), err, "error while waiting for package build to succeed:%w", err)

	_, err = cli.ExecCommand(f.framework, f.ctx, "function", "create", "--name", testDeployFuncName, "--pkg", testDeployPkg, "--entrypoint", "hello.main")
	require.NoError(f.T(), err)

	err = f.fetcherClient.Fetch(f.ctx, &fetcher.FunctionFetchRequest{
		Filename:      "hello.py",
		StorageSvcUrl: f.storagesvcURL,
		KeepArchive:   true,
		FetchType:     fv1.FETCH_DEPLOYMENT,
		Package: metav1.ObjectMeta{
			Name:      testDeployPkg,
			Namespace: "default",
		},
	})
	require.NoError(f.T(), err)

	file, err := os.OpenFile(f.sharedVolDir+"/hello.py", os.O_RDONLY, 0444)
	require.NoError(f.T(), err, "fetched file does not exist in fetcher directory")
	defer file.Close()

	_, err = cli.ExecCommand(f.framework, f.ctx, "function", "delete", "--name", testDeployFuncName)
	require.NoError(f.T(), err)
}

func (f *FetcherTestSuite) TestFetcherURLType() {

	const testDeployPkg = "test-url-arch-pkg"
	const testDeployArchiveFile = "data/test-url-arch.zip"
	const testDeployFuncName = "url-arch-test"

	_, err := cli.ExecCommand(f.framework, f.ctx, "package", "create", "--name", testDeployPkg, "--deployarchive", testDeployArchiveFile, "--env", f.envName)
	require.NoError(f.T(), err, "error in creating package:%s in environment:%s", testDeployPkg, f.envName)
	err = wait.PollUntilContextTimeout(f.ctx, 10*time.Second, 5*time.Minute, true, func(c context.Context) (done bool, err error) {

		pkgs, err := f.fissionClient.CoreV1().Packages(metav1.NamespaceDefault).List(c, metav1.ListOptions{})

		if err != nil {
			return false, err
		}

		for _, pkg := range pkgs.Items {
			if pkg.Name == testDeployPkg && pkg.Status.BuildStatus == fv1.BuildStatusSucceeded {
				return true, nil
			} else if pkg.Name == testDeployPkg && pkg.Status.BuildStatus == fv1.BuildStatusFailed {
				return true, fmt.Errorf("build failed for package:%s", pkg.Name)
			}
		}

		return false, nil
	})
	require.NoError(f.T(), err, "error while waiting for package build to succeed:%w", err)

	_, err = cli.ExecCommand(f.framework, f.ctx, "function", "create", "--name", testDeployFuncName, "--pkg", testDeployPkg, "--entrypoint", "hello.main")
	require.NoError(f.T(), err)

	err = f.fetcherClient.Fetch(f.ctx, &fetcher.FunctionFetchRequest{
		Filename:      "new.py",
		StorageSvcUrl: f.storagesvcURL,
		KeepArchive:   true,
		FetchType:     fv1.FETCH_URL,
		Url:           "http://localhost:8888/files/data/test-url-arch.zip",
		Package: metav1.ObjectMeta{
			Name:      testDeployPkg,
			Namespace: "default",
		},
	})
	require.NoError(f.T(), err)

	file, err := os.OpenFile(f.sharedVolDir+"/new.py", os.O_RDONLY, 0444)
	require.NoError(f.T(), err, "fetched file does not exist in fetcher directory")
	defer file.Close()

	_, err = cli.ExecCommand(f.framework, f.ctx, "function", "delete", "--name", testDeployFuncName)
	require.NoError(f.T(), err)
}

func (f *FetcherTestSuite) TestFetcherUpload() {

	storageClient := storageClient.MakeClient(f.storagesvcURL)

	resp, err := f.fetcherClient.Upload(context.Background(), &fetcher.ArchiveUploadRequest{
		Filename:       "hello.js",
		StorageSvcUrl:  f.storagesvcURL,
		ArchivePackage: true,
	})
	require.NoError(f.T(), err)

	archiveID, err := getArchiveIDFromURL(resp.ArchiveDownloadUrl)
	require.NoError(f.T(), err)

	filesIds, err := storageClient.List(f.ctx)
	require.NoError(f.T(), err)

	idFound := contains(filesIds, archiveID)
	require.True(f.T(), idFound, "archive id not found in storagesvc list")

	resp, err = f.fetcherClient.Upload(context.Background(), &fetcher.ArchiveUploadRequest{
		Filename:       "hello.js",
		StorageSvcUrl:  f.storagesvcURL,
		ArchivePackage: false,
	})
	require.NoError(f.T(), err)

	archiveID, err = getArchiveIDFromURL(resp.ArchiveDownloadUrl)
	require.NoError(f.T(), err)

	filesIds, err = storageClient.List(f.ctx)
	require.NoError(f.T(), err)

	idFound = contains(filesIds, archiveID)
	require.True(f.T(), idFound, "archive id not found in storagesvc list")
}

func (f *FetcherTestSuite) TestFetcherSpecialize() {

	const testDeployPkg = "test-deploy-pkg-specialize"
	const testDeployArchiveFile = "data/test-specialize-deploy-pkg.zip"
	const testDeployFuncName = "deploypy-specialize"

	configMapData := map[string]string{
		"env": "test",
	}

	configMapName := "test-configmap"

	configMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: metav1.NamespaceDefault,
		},
		Data: configMapData,
	}

	cfgMap, err := f.k8sClient.CoreV1().ConfigMaps(metav1.NamespaceDefault).Create(f.ctx, configMap, metav1.CreateOptions{})
	require.NoError(f.T(), err)

	secretName := "test-secret"
	secretData := map[string][]byte{
		"username": []byte("admin"),
	}

	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: metav1.NamespaceDefault,
		},
		Data: secretData,
	}

	secret, err = f.k8sClient.CoreV1().Secrets(metav1.NamespaceDefault).Create(f.ctx, secret, metav1.CreateOptions{})
	require.NoError(f.T(), err)

	_, err = cli.ExecCommand(f.framework, f.ctx, "package", "create", "--name", testDeployPkg, "--deployarchive", testDeployArchiveFile, "--env", f.envName)
	require.NoError(f.T(), err, "error in creating package:%s in environment:%s", testDeployPkg, f.envName)
	err = wait.PollUntilContextTimeout(f.ctx, 10*time.Second, 5*time.Minute, true, func(c context.Context) (done bool, err error) {

		pkgs, err := f.fissionClient.CoreV1().Packages(metav1.NamespaceDefault).List(c, metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		for _, pkg := range pkgs.Items {
			if pkg.Name == testDeployPkg && pkg.Status.BuildStatus == fv1.BuildStatusSucceeded {
				return true, nil
			} else if pkg.Name == testDeployPkg && pkg.Status.BuildStatus == fv1.BuildStatusFailed {
				return true, fmt.Errorf("build failed for package:%s", pkg.Name)
			}
		}

		return false, nil
	})
	require.NoError(f.T(), err, "error while waiting for package build to succeed:%w", err)

	_, err = cli.ExecCommand(f.framework, f.ctx, "function", "create", "--name", testDeployFuncName, "--pkg", testDeployPkg, "--entrypoint", "hello.main")
	require.NoError(f.T(), err)

	// test with EnvVersion v2
	err = f.fetcherClient.Specialize(f.ctx, &fetcher.FunctionSpecializeRequest{
		FetchReq: fetcher.FunctionFetchRequest{
			Filename:      "hi.py",
			StorageSvcUrl: f.storagesvcURL,
			KeepArchive:   true,
			FetchType:     fv1.FETCH_DEPLOYMENT,
			Package: metav1.ObjectMeta{
				Name:      testDeployPkg,
				Namespace: "default",
			},
			ConfigMaps: []fv1.ConfigMapReference{{
				Name:      cfgMap.Name,
				Namespace: cfgMap.Namespace,
			}},
			Secrets: []fv1.SecretReference{{
				Name:      secret.Name,
				Namespace: secret.Namespace,
			}},
		},
		LoadReq: fetcher.FunctionLoadRequest{
			EnvVersion:   2,
			FilePath:     f.sharedVolDir + "./hi.py",
			FunctionName: testDeployFuncName,
			FunctionMetadata: &metav1.ObjectMeta{
				Name:      testDeployFuncName,
				Namespace: metav1.NamespaceDefault,
			},
		},
	})
	require.NoError(f.T(), err)

	file, err := os.OpenFile(f.sharedVolDir+"/hi.py", os.O_RDONLY, 0444)
	require.NoError(f.T(), err, "fetched file does not exist in fetcher directory")
	defer file.Close()

	// test with no EnvVersion
	err = f.fetcherClient.Specialize(f.ctx, &fetcher.FunctionSpecializeRequest{
		FetchReq: fetcher.FunctionFetchRequest{
			Filename:      "hi.py",
			StorageSvcUrl: f.storagesvcURL,
			KeepArchive:   true,
			FetchType:     fv1.FETCH_DEPLOYMENT,
			Package: metav1.ObjectMeta{
				Name:      testDeployPkg,
				Namespace: "default",
			},
		},
		LoadReq: fetcher.FunctionLoadRequest{
			FilePath:     f.sharedVolDir + "./hi.py",
			FunctionName: testDeployFuncName,
			FunctionMetadata: &metav1.ObjectMeta{
				Name:      testDeployFuncName,
				Namespace: metav1.NamespaceDefault,
			},
		},
	})
	require.NoError(f.T(), err)

	// set throwError to true to test for error case
	f.specTestData.throwError = true

	err = f.fetcherClient.Specialize(f.ctx, &fetcher.FunctionSpecializeRequest{
		FetchReq: fetcher.FunctionFetchRequest{
			Filename:      "hi.py",
			StorageSvcUrl: f.storagesvcURL,
			KeepArchive:   true,
			FetchType:     fv1.FETCH_DEPLOYMENT,
			Package: metav1.ObjectMeta{
				Name:      testDeployPkg,
				Namespace: "default",
			},
		},
		LoadReq: fetcher.FunctionLoadRequest{
			FilePath:     f.sharedVolDir + "./hi.py",
			FunctionName: testDeployFuncName,
			FunctionMetadata: &metav1.ObjectMeta{
				Name:      testDeployFuncName,
				Namespace: metav1.NamespaceDefault,
			},
		},
	})
	require.Error(f.T(), err)

	_, err = cli.ExecCommand(f.framework, f.ctx, "function", "delete", "--name", testDeployFuncName)
	require.NoError(f.T(), err)
}

func (f *FetcherTestSuite) TearDownSuite() {
	_, err := cli.ExecCommand(f.framework, f.ctx, "env", "delete", "--name", f.envName)
	require.NoError(f.T(), err)

	err = wait.PollUntilContextTimeout(f.ctx, 2*time.Second, 5*time.Minute, true, func(c context.Context) (done bool, err error) {

		deploys, err := f.k8sClient.AppsV1().Deployments(metav1.NamespaceDefault).List(c, metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		if len(deploys.Items) == 0 {
			return true, nil
		}
		for _, deploy := range deploys.Items {
			if strings.Contains(deploy.Name, f.envName) {
				return false, nil
			}
		}

		return true, nil
	})
	require.NoError(f.T(), err)

	f.cancel()
	f.logger.Sync()
	f.mgr.Wait()
	err = f.framework.Stop()
	require.NoError(f.T(), err, "error stopping framework")

	err = os.RemoveAll(f.sharedVolDir)
	require.NoError(f.T(), err, "error removing fetcher shared vol directory")

	err = os.RemoveAll(f.cfgMapsDir)
	require.NoError(f.T(), err, "error removing fetcher cfgMaps directory")

	err = os.RemoveAll(f.secretsDir)
	require.NoError(f.T(), err, "error removing fetcher secrets vol directory")
}

func (f *FetcherTestSuite) createFetcherTestDirs() error {

	cfgMapsDir, err := os.MkdirTemp("/tmp", "fetcher-cfgmaps")
	if err != nil {
		return fmt.Errorf("error creating temp directory for config maps: %w", err)
	}

	f.cfgMapsDir = cfgMapsDir

	secretsDir, err := os.MkdirTemp("/tmp", "fetcher-secrets")
	if err != nil {
		return fmt.Errorf("error creating temp directory for config maps: %w", err)
	}

	f.secretsDir = secretsDir

	shareVolDir, err := os.MkdirTemp("/tmp", "fetcher-shared")
	if err != nil {
		return fmt.Errorf("error creating temp directory for config maps: %w", err)
	}
	f.sharedVolDir = shareVolDir

	file, err := os.Create(shareVolDir + "/hello.js")
	if err != nil {
		return fmt.Errorf("error creating test file in shared volume directory: %w", err)
	}
	defer file.Close()

	_, err = file.Write([]byte(testFileData))
	if err != nil {
		return fmt.Errorf("error writing data to test file in shared volume directory: %w", err)
	}

	return nil
}

func (f *FetcherTestSuite) setupPodInfoMountDir() error {

	podInfoDir, err := os.MkdirTemp("/tmp", "fetcher-pod-info")
	if err != nil {
		return fmt.Errorf("error creating temp directory for fetcher pod info: %w", err)
	}

	f.podInfoDir = podInfoDir

	podNameFile, err := os.Create(podInfoDir + "/name")
	if err != nil {
		return fmt.Errorf("error creating name file in fetcher pod info directory: %w", err)
	}
	defer podNameFile.Close()

	_, err = podNameFile.Write([]byte("test"))
	if err != nil {
		return fmt.Errorf("error writing data in name file in fetcher pod info directory: %w", err)
	}

	podNamespaceFile, err := os.Create(podInfoDir + "/namespace")
	if err != nil {
		return fmt.Errorf("error creating namespace file in fetcher pod info directory: %w", err)
	}
	defer podNamespaceFile.Close()

	_, err = podNamespaceFile.Write([]byte("test-ns"))
	if err != nil {
		return fmt.Errorf("error writing data in namespace file in fetcher pod info directory: %w", err)
	}

	return nil
}

func getArchiveIDFromURL(archiveDownloadURL string) (string, error) {

	downloadURL, err := url.Parse(archiveDownloadURL)
	if err != nil {
		return "", err
	}

	querParams, err := url.ParseQuery(downloadURL.RawQuery)
	if err != nil {
		return "", err
	}

	idParamValues := querParams["id"]
	if len(idParamValues) == 0 {
		return "", errors.New("id param not present in archiveDownloadURL")
	}

	return idParamValues[0], nil
}

func contains(list []string, item string) bool {

	for _, listItem := range list {
		if item == listItem {
			return true
		}
	}

	return false
}

func TestFetcherTestSuite(t *testing.T) {
	suite.Run(t, new(FetcherTestSuite))
}
