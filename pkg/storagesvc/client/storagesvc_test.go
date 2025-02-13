/*
Copyright 2017 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package client

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"testing"
	"time"

	"github.com/dchest/uniuri"
	"github.com/minio/minio-go"
	"github.com/ory/dockertest"
	dc "github.com/ory/dockertest/docker"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/storagesvc"
	"github.com/fission/fission/pkg/utils/manager"
)

const (
	minioAccessKeyID     = "minioadmin"
	minioSecretAccessKey = "minioadmin"
	minioRegion          = "ap-south-1"
)

func failTest(t *testing.T, err error) {
	if err != nil {
		t.Fatalf("%v", err)
	}
}

func MakeTestFile(size int) (*os.File, error) {
	f, err := os.CreateTemp("", "storagesvc_test_")

	if err != nil {
		return nil, err
	}

	_, err = f.Write(bytes.Repeat([]byte("."), size))

	if err != nil {
		return nil, err
	}
	return f, nil
}

func runMinioDockerContainer(pool *dockertest.Pool) *dockertest.Resource {
	options := &dockertest.RunOptions{
		Repository: "minio/minio",
		Tag:        "latest",
		Cmd:        []string{"server", "/data"},
		PortBindings: map[dc.Port][]dc.PortBinding{
			"9000/tcp": {{HostIP: "", HostPort: "9000"}},
		},
	}

	// pulls an image, creates a container based on it and runs it
	resource, err := pool.RunWithOptions(options)
	if err != nil {
		log.Fatalf("Could not start resource: %s", err)
	}
	return resource
}

func TestS3StorageService(t *testing.T) {
	fmt.Println("Test S3 Storage service")
	var minioClient *minio.Client

	mgr := manager.New()
	defer mgr.Wait()

	// Start minio docker container
	pool, err := dockertest.NewPool("")
	resource := runMinioDockerContainer(pool)

	endpoint := fmt.Sprintf("localhost:%s", resource.GetPort("9000/tcp"))

	if err := pool.Retry(func() error {
		minioClient, err = minio.New(endpoint, minioAccessKeyID, minioSecretAccessKey, false)
		if err != nil {
			return err
		}

		// This is to ensure container is up. Just getting minioClient
		// isn't sufficient to assume container is up.
		_, err = minioClient.ListBuckets()
		if err != nil {
			return err
		}

		return nil
	}); err != nil {
		log.Fatalf("Could not connect to docker: %s", err)
	}

	defer func() {
		err := pool.Purge(resource)
		if err != nil {
			log.Fatal(err)
		}
	}()

	// Start storagesvc
	bucketName := "test-s3-service"
	subDir := "x/y/z"
	// testID := uniuri.NewLen(8)
	port := 8081

	config := zap.NewDevelopmentConfig()
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	logger, err := config.Build()
	failTest(t, err)

	log.Println("starting storage svc")
	os.Setenv("STORAGE_S3_ENDPOINT", endpoint)
	os.Setenv("STORAGE_S3_BUCKET_NAME", bucketName)
	os.Setenv("STORAGE_S3_SUB_DIR", subDir)
	os.Setenv("STORAGE_S3_ACCESS_KEY_ID", minioAccessKeyID)
	os.Setenv("STORAGE_S3_SECRET_ACCESS_KEY", minioSecretAccessKey)
	os.Setenv("STORAGE_S3_REGION", minioRegion)

	storage := storagesvc.NewS3Storage()
	ctx := t.Context()
	_ = storagesvc.Start(ctx, crd.NewClientGenerator(), logger, storage, mgr, port)

	time.Sleep(time.Second)
	client := MakeClient(fmt.Sprintf("http://localhost:%v/", 8081))

	// generate a test file
	tmpfile, err := MakeTestFile(10 * 1024)
	failTest(t, err)
	defer os.Remove(tmpfile.Name())

	// store it
	metadata := make(map[string]string)
	fileID, err := client.Upload(ctx, tmpfile.Name(), &metadata)
	failTest(t, err)

	time.Sleep(10 * time.Second)

	// Retrieve file through minioClient
	reader, err := minioClient.GetObject(bucketName, fileID, minio.GetObjectOptions{})
	failTest(t, err)
	defer reader.Close()

	retThroughMinio, err := os.CreateTemp("", "storagesvc_verify_minio_")
	failTest(t, err)
	defer os.Remove(retThroughMinio.Name())

	stat, err := reader.Stat()
	failTest(t, err)

	if _, err := io.CopyN(retThroughMinio, reader, stat.Size); err != nil {
		log.Fatalln(err)
	}

	// Retrieve file through API
	retThroughAPI, err := os.CreateTemp("", "storagesvc_verify_")
	failTest(t, err)
	os.Remove(retThroughAPI.Name())

	err = client.Download(ctx, fileID, retThroughAPI.Name())
	failTest(t, err)
	defer os.Remove(retThroughAPI.Name())

	// compare contents
	contentsMinio, err := os.ReadFile(retThroughMinio.Name())
	failTest(t, err)
	contentsAPI, err := os.ReadFile(retThroughAPI.Name())
	failTest(t, err)
	if !bytes.Equal(contentsMinio, contentsAPI) {
		log.Panic("Contents don't match")
	}

	// delete uploaded file
	err = client.Delete(ctx, fileID)
	failTest(t, err)

	// make sure download fails
	err = client.Download(ctx, fileID, "xxx")
	if err == nil {
		log.Panic("Download succeeded but file isn't supposed to exist")
	}
}

func TestLocalStorageService(t *testing.T) {
	testID := uniuri.NewLen(8)
	port := 8082

	mgr := manager.New()
	defer mgr.Wait()

	config := zap.NewDevelopmentConfig()
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	logger, err := config.Build()
	failTest(t, err)

	log.Println("starting storage svc")
	localPath := fmt.Sprintf("/tmp/%v", testID)
	_ = os.Mkdir(localPath, os.ModePerm)
	storage := storagesvc.NewLocalStorage(localPath)
	ctx := t.Context()
	os.Setenv("METRICS_ADDR", "8083")
	_ = storagesvc.Start(ctx, crd.NewClientGenerator(), logger, storage, mgr, port)

	time.Sleep(time.Second)
	client := MakeClient(fmt.Sprintf("http://localhost:%v/", port))

	// generate a test file
	tmpfile, err := MakeTestFile(10 * 1024)
	failTest(t, err)
	defer os.Remove(tmpfile.Name())

	// store it
	metadata := make(map[string]string)
	fileID, err := client.Upload(ctx, tmpfile.Name(), &metadata)
	failTest(t, err)

	ids, err := client.List(ctx)
	if err != nil {
		t.Fatalf("Could not list files: %s", err)
	}
	if len(ids) != 1 {
		t.Fatalf("Expected 1 file, got %v", len(ids))
	}

	// make a temp file for verification
	retrievedfile, err := os.CreateTemp("", "storagesvc_verify_")
	failTest(t, err)
	os.Remove(retrievedfile.Name())

	// retrieve uploaded file
	err = client.Download(ctx, fileID, retrievedfile.Name())
	failTest(t, err)
	defer os.Remove(retrievedfile.Name())

	// compare contents
	contents1, err := os.ReadFile(tmpfile.Name())
	failTest(t, err)
	contents2, err := os.ReadFile(retrievedfile.Name())
	failTest(t, err)
	if !bytes.Equal(contents1, contents2) {
		log.Panic("Contents don't match")
	}

	// delete uploaded file
	err = client.Delete(ctx, fileID)
	failTest(t, err)

	// make sure download fails
	err = client.Download(ctx, fileID, "xxx")
	if err == nil {
		log.Panic("Download succeeded but file isn't supposed to exist")
	}

	// // cleanup /tmp
	os.RemoveAll(fmt.Sprintf("/tmp/%v", testID))
}
