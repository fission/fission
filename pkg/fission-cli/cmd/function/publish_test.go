// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/versioning"
)

func publishedVersion() *fv1.FunctionVersion {
	return &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v1", Namespace: "default"},
		Spec:       fv1.FunctionVersionSpec{FunctionName: "hello", Sequence: 1},
	}
}

func TestPrintPublishResultName(t *testing.T) {
	var buf bytes.Buffer
	err := printPublishResult(&buf, &versioning.PublishResult{Version: publishedVersion(), Created: true}, "name")
	require.NoError(t, err)
	assert.Equal(t, "hello-v1\n", buf.String())
}

func TestPrintPublishResultJSON(t *testing.T) {
	out := captureStdout(t, func() error {
		var buf bytes.Buffer
		return printPublishResult(&buf, &versioning.PublishResult{Version: publishedVersion(), Created: true}, "json")
	})

	var got fv1.FunctionVersion
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "hello-v1", got.Name)
}

func TestPrintPublishResultTableCreated(t *testing.T) {
	var buf bytes.Buffer
	err := printPublishResult(&buf, &versioning.PublishResult{Version: publishedVersion(), Created: true}, "")
	require.NoError(t, err)
	assert.Equal(t, "created hello-v1\n", buf.String())
}

func TestPrintPublishResultTableUnchanged(t *testing.T) {
	var buf bytes.Buffer
	err := printPublishResult(&buf, &versioning.PublishResult{Version: publishedVersion(), Created: false}, "")
	require.NoError(t, err)
	assert.Equal(t, "unchanged hello-v1\n", buf.String())
}

func TestPrintPublishResultInvalidFormatErrors(t *testing.T) {
	var buf bytes.Buffer
	err := printPublishResult(&buf, &versioning.PublishResult{Version: publishedVersion(), Created: true}, "bogus")
	require.Error(t, err)
}

func packageWithStatus(status fv1.BuildStatus) *fv1.Package {
	return &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-pkg", Namespace: "default"},
		Status:     fv1.PackageStatus{BuildStatus: status},
	}
}

func TestWaitForPackageBuildReadyImmediately(t *testing.T) {
	for _, status := range []fv1.BuildStatus{fv1.BuildStatusSucceeded, fv1.BuildStatusNone} {
		t.Run(string(status), func(t *testing.T) {
			fc := fissionfake.NewSimpleClientset(packageWithStatus(status)) //nolint:staticcheck
			err := waitForPackageBuild(t.Context(), fc, "default", "hello-pkg", time.Second)
			require.NoError(t, err)
		})
	}
}

func TestWaitForPackageBuildFailedReturnsImmediately(t *testing.T) {
	fc := fissionfake.NewSimpleClientset(packageWithStatus(fv1.BuildStatusFailed)) //nolint:staticcheck
	err := waitForPackageBuild(t.Context(), fc, "default", "hello-pkg", time.Second)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "build failed"), "got: %v", err)
}

func TestWaitForPackageBuildTimesOut(t *testing.T) {
	fc := fissionfake.NewSimpleClientset(packageWithStatus(fv1.BuildStatusRunning)) //nolint:staticcheck
	err := waitForPackageBuild(t.Context(), fc, "default", "hello-pkg", 20*time.Millisecond)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "timed out"), "got: %v", err)
}

func TestWaitForPackageBuildMissingPackageTimesOut(t *testing.T) {
	fc := fissionfake.NewSimpleClientset() //nolint:staticcheck
	err := waitForPackageBuild(t.Context(), fc, "default", "missing-pkg", 20*time.Millisecond)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "timed out"), "got: %v", err)
}
