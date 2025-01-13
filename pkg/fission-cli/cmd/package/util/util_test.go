package util

import (
	"bytes"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestPrintPackageSummary(t *testing.T) {

	pkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{
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

func TestValidArchiveURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected bool
	}{
		{
			name:     "Valid Archive URL",
			url:      "http://storagesvc.fission/v1/archive?id=/fission/fission-functions/Fc4c15f47-bb49-47c5-b382-526a6539841d",
			expected: true,
		},
		{
			name:     "Invalid Archive URL",
			url:      "https://raw.githubusercontent.com/imaginery/training/refs/heads/fission/hello-go?token=ABCD",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validArchiveURL(tt.url)
			if err != nil {
				t.Errorf("got error %v", err)
			}

			if got != tt.expected {
				t.Errorf("expected %t got %t", got, tt.expected)
			}
		})
	}
}
