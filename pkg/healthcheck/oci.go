// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package healthcheck

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	executorUtils "github.com/fission/fission/pkg/executor/util"
)

// OCI package-delivery checks: catch the registry misconfigurations that
// otherwise surface planes away from their cause (a build "succeeds" but
// functions fail to mount the image, etc.).

// deployEnv reads a container env var from a Deployment in the fission
// namespace ("" when unset or the deployment is missing).
func deployEnv(ctx context.Context, kube kubernetes.Interface, ns, deploy, key string) string {
	d, err := kube.AppsV1().Deployments(ns).Get(ctx, deploy, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	for _, c := range d.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == key {
				return e.Value
			}
		}
	}
	return ""
}

// isClusterLocalHost reports whether a registry prefix's host is resolvable
// only by cluster DNS — a name the kubelet (which pulls image volumes via
// the NODE resolver) cannot resolve.
func isClusterLocalHost(prefix string) bool {
	host := prefix
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return strings.HasSuffix(host, ".svc") || strings.Contains(host, ".svc.")
}

// CheckOCIProducer validates the package-registry (producer) configuration.
func (hc *HealthChecker) CheckOCIProducer(ctx context.Context) error {
	kube := hc.kubeAPI
	if deployEnv(ctx, kube, hc.fissionNamespace, "buildermgr", "PACKAGE_REGISTRY_ENABLED") != "true" {
		return nil // producer off: builds store tarballs; nothing to check
	}
	prefix := deployEnv(ctx, kube, hc.fissionNamespace, "buildermgr", "PACKAGE_REGISTRY_REPOSITORY_PREFIX")
	published := deployEnv(ctx, kube, hc.fissionNamespace, "buildermgr", "PACKAGE_REGISTRY_PUBLISHED_PREFIX")
	if prefix == "" {
		return fmt.Errorf("packageRegistry is enabled but repositoryPrefix is empty")
	}
	effective := published
	if effective == "" {
		effective = prefix
	}
	if isClusterLocalHost(effective) {
		return fmt.Errorf("packages will reference %q, a cluster-DNS name nodes cannot resolve — image-volume mounts will fail; set packageRegistry.publishedPrefix to a node-resolvable name (e.g. a NodePort or external registry address)", effective)
	}
	return nil
}

// CheckOCIImageVolume reports whether image-volume delivery is actually
// active: the flag may be on while the cluster is too old, which silently
// (by design) degrades to fetcher pulls.
func (hc *HealthChecker) CheckOCIImageVolume(ctx context.Context) error {
	kube := hc.kubeAPI
	if deployEnv(ctx, kube, hc.fissionNamespace, "executor", "ENABLE_OCI_IMAGE_VOLUME") != "true" {
		return nil // explicitly off: fetcher-pull delivery, valid configuration
	}
	// Defer to the SAME authority the executor's runtime gate uses, so the
	// healthcheck can never report "OK" while the runtime has silently
	// degraded to fetcher pulls (e.g. on an unparsable vendor version).
	supported, err := executorUtils.ImageVolumeSupported(kube.Discovery())
	if err != nil {
		return fmt.Errorf("image volumes are enabled but the cluster version could not be confirmed (%w): OCI packages may be using fetcher pulls instead of image volumes", err)
	}
	if !supported {
		ver, _ := kube.Discovery().ServerVersion()
		return fmt.Errorf("image volumes are enabled but the cluster is v%s.%s (< 1.33): OCI packages will use fetcher pulls instead — works, but without the image-volume cold-start benefit", ver.Major, ver.Minor)
	}
	return nil
}
