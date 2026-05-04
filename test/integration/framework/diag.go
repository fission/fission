//go:build integration

package framework

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// dumpDiagnostics is invoked from the namespace cleanup hook on test failure.
// It writes pod descriptions, events, container logs, and Fission CR YAML to
// $LOG_DIR/<test>/ so CI artifact upload can capture them.
func (ns *TestNamespace) dumpDiagnostics() {
	dir := filepath.Join(logDir(), sanitize(ns.t.Name()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		ns.t.Logf("diag: mkdir %s: %v", dir, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ns.dumpEvents(ctx, dir)
	ns.dumpPods(ctx, dir)
	ns.dumpFissionCRs(ctx, dir)
	ns.t.Logf("diag: wrote diagnostics for failed test to %s", dir)
}

func (ns *TestNamespace) dumpEvents(ctx context.Context, dir string) {
	events, err := ns.f.kubeClient.CoreV1().Events(ns.Name).List(ctx, metav1.ListOptions{})
	if err != nil {
		ns.t.Logf("diag: list events: %v", err)
		return
	}
	writeYAML(ns.t, filepath.Join(dir, "events.yaml"), events)
}

func (ns *TestNamespace) dumpPods(ctx context.Context, dir string) {
	pods, err := ns.f.kubeClient.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{})
	if err != nil {
		ns.t.Logf("diag: list pods: %v", err)
		return
	}
	writeYAML(ns.t, filepath.Join(dir, "pods.yaml"), pods)

	for _, p := range pods.Items {
		containers := append([]corev1.Container{}, p.Spec.InitContainers...)
		containers = append(containers, p.Spec.Containers...)
		for _, c := range containers {
			ns.dumpContainerLog(ctx, dir, p.Name, c.Name)
		}
	}
}

func (ns *TestNamespace) dumpContainerLog(ctx context.Context, dir, pod, container string) {
	req := ns.f.kubeClient.CoreV1().Pods(ns.Name).GetLogs(pod, &corev1.PodLogOptions{Container: container})
	stream, err := req.Stream(ctx)
	if err != nil {
		ns.t.Logf("diag: logs %s/%s: %v", pod, container, err)
		return
	}
	defer stream.Close()
	f, err := os.Create(filepath.Join(dir, fmt.Sprintf("logs-%s-%s.log", pod, container)))
	if err != nil {
		ns.t.Logf("diag: create log file: %v", err)
		return
	}
	defer f.Close()
	if _, err := io.Copy(f, stream); err != nil {
		ns.t.Logf("diag: write logs %s/%s: %v", pod, container, err)
	}
}

func (ns *TestNamespace) dumpFissionCRs(ctx context.Context, dir string) {
	fc := ns.f.fissionClient.CoreV1()
	if envs, err := fc.Environments(ns.Name).List(ctx, metav1.ListOptions{}); err == nil {
		writeYAML(ns.t, filepath.Join(dir, "environments.yaml"), envs)
	}
	if fns, err := fc.Functions(ns.Name).List(ctx, metav1.ListOptions{}); err == nil {
		writeYAML(ns.t, filepath.Join(dir, "functions.yaml"), fns)
	}
	if pkgs, err := fc.Packages(ns.Name).List(ctx, metav1.ListOptions{}); err == nil {
		writeYAML(ns.t, filepath.Join(dir, "packages.yaml"), pkgs)
	}
	if hts, err := fc.HTTPTriggers(ns.Name).List(ctx, metav1.ListOptions{}); err == nil {
		writeYAML(ns.t, filepath.Join(dir, "httptriggers.yaml"), hts)
	}
}

func writeYAML(t *testing.T, path string, obj any) {
	b, err := yaml.Marshal(obj)
	if err != nil {
		t.Logf("diag: marshal %s: %v", path, err)
		return
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Logf("diag: write %s: %v", path, err)
	}
}

func logDir() string {
	if v := os.Getenv("LOG_DIR"); v != "" {
		return v
	}
	return filepath.Join("test", "integration", "logs")
}
