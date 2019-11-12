package util

import (
	"bytes"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
)

func TestPrintPackageSummary(t *testing.T) {

	pkg := &fv1.Package{
		Metadata: metav1.ObjectMeta{
			Name:      "foobar",
			Namespace: "dummy",
		},
		Status: fv1.PackageStatus{
			BuildStatus: "failed",
			BuildLog:    "dummy-build-log",
		},
	}

	expected := `Name:        foobar\nEnvironment: \nStatus:      failed\nBuild Logs:\ndummy-build-log`
	writer := &bytes.Buffer{}
	PrintPackageSummary(writer, pkg)

	gotWriter := strings.ReplaceAll(writer.String(), "\n", `\n`)
	if gotWriter != expected {
		t.Errorf("PrintPackageBuildLog() = %v, want %v", gotWriter, expected)
	}
}
