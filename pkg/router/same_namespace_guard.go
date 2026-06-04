// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	k8scache "k8s.io/client-go/tools/cache"
)

// EnforceSameNamespaceInvocationEnv toggles the same-namespace guard on the
// router's internal listener. When "true", a caller may only invoke a function
// in its OWN namespace through /fission-function/<ns>/<name>, unless the caller
// is an internal Fission component (a pod in the install namespace). This closes
// the cross-namespace invocation hole: a function pod in one tenant namespace
// can otherwise reach a private function in another namespace via the shared,
// (when internalAuth is disabled) unauthenticated internal listener.
const EnforceSameNamespaceInvocationEnv = "ROUTER_ENFORCE_SAME_NAMESPACE_INVOCATION"

// callerNamespaceLookup resolves a caller pod IP to its namespace.
type callerNamespaceLookup interface {
	lookup(ip string) (namespace string, found bool)
}

// sameNamespaceGuard enforces same-namespace (or internal-component) invocation
// on the internal listener. It is applied per function handler so the target
// namespace comes unambiguously from the function (UrlForFunction folds the
// default namespace into a single-segment path, so the path itself is ambiguous).
type sameNamespaceGuard struct {
	lookup           callerNamespaceLookup
	installNamespace string
	logger           logr.Logger
}

// wrap returns inner guarded so it only serves callers whose namespace is
// targetNamespace, or the install namespace (internal Fission components such as
// timer / kubewatcher / mqtrigger / executor, which legitimately invoke
// functions in any namespace). Everything else gets 403.
func (g *sameNamespaceGuard) wrap(inner http.Handler, targetNamespace string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callerIP := clientIP(r.RemoteAddr)
		callerNS, found := g.lookup.lookup(callerIP)
		if !found {
			// Fail closed: a source we cannot attribute to a namespace must not be
			// allowed to invoke cross-namespace. Pods — internal components and
			// functions alike — all have resolvable IPs.
			g.logger.Info("rejecting internal invocation: caller namespace unresolved",
				"caller_ip", callerIP, "target_namespace", targetNamespace, "path", r.URL.Path)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if callerNS != g.installNamespace && callerNS != targetNamespace {
			g.logger.Info("rejecting cross-namespace internal invocation",
				"caller_namespace", callerNS, "target_namespace", targetNamespace,
				"caller_ip", callerIP, "path", r.URL.Path)
			http.Error(w, "forbidden: cross-namespace function invocation is not allowed", http.StatusForbidden)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// clientIP extracts the IP from a net/http RemoteAddr ("ip:port"). X-Forwarded-For
// is deliberately ignored: the internal listener is reached pod-to-pod, so the TCP
// peer IP is the trustworthy caller identity; a forwarded header would be
// caller-controlled and trivially spoofable.
func clientIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// podRef is the owning pod of a cached IP, so a delete for a recycled IP only
// removes the mapping when it still belongs to the pod being deleted.
type podRef struct {
	namespace string
	name      string
}

// podIPCache maintains an in-memory podIP -> namespace index from a cluster-wide
// pod informer, so the guard attributes a caller IP to a namespace in O(1)
// without a per-request API call. A transform keeps only namespace/name/podIP to
// bound memory.
type podIPCache struct {
	mu      sync.RWMutex
	ipToPod map[string]podRef
}

func (c *podIPCache) lookup(ip string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.ipToPod[ip]
	return p.namespace, ok
}

func (c *podIPCache) set(ip string, p podRef) {
	if ip == "" {
		return
	}
	c.mu.Lock()
	c.ipToPod[ip] = p
	c.mu.Unlock()
}

// del removes ip only if it still maps to name, so a delete event for an old pod
// that already had its IP recycled by a new pod does not evict the new mapping.
func (c *podIPCache) del(ip, name string) {
	if ip == "" {
		return
	}
	c.mu.Lock()
	if cur, ok := c.ipToPod[ip]; ok && cur.name == name {
		delete(c.ipToPod, ip)
	}
	c.mu.Unlock()
}

// newPodIPCache starts a cluster-wide pod informer and blocks until its cache has
// synced, returning a ready resolver. Cluster-wide is required: a caller may be a
// function pod in any namespace, not just the Fission-watched ones.
func newPodIPCache(ctx context.Context, kubeClient kubernetes.Interface, logger logr.Logger) (*podIPCache, error) {
	c := &podIPCache{ipToPod: make(map[string]podRef)}
	factory := informers.NewSharedInformerFactory(kubeClient, 30*time.Minute)
	informer := factory.Core().V1().Pods().Informer()
	if err := informer.SetTransform(func(obj any) (any, error) {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			return obj, nil
		}
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: pod.Name},
			Status:     corev1.PodStatus{PodIP: pod.Status.PodIP},
		}, nil
	}); err != nil {
		return nil, fmt.Errorf("error setting pod informer transform: %w", err)
	}
	if _, err := informer.AddEventHandler(k8scache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { c.onPod(obj) },
		UpdateFunc: func(_, obj any) { c.onPod(obj) },
		DeleteFunc: func(obj any) { c.onPodDelete(obj) },
	}); err != nil {
		return nil, fmt.Errorf("error adding pod informer handler: %w", err)
	}
	factory.Start(ctx.Done())
	if !k8scache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		return nil, fmt.Errorf("timed out waiting for router pod-ip cache sync")
	}
	logger.Info("router same-namespace guard: pod-ip cache synced")
	return c, nil
}

func (c *podIPCache) onPod(obj any) {
	if pod, ok := obj.(*corev1.Pod); ok {
		c.set(pod.Status.PodIP, podRef{namespace: pod.Namespace, name: pod.Name})
	}
}

func (c *podIPCache) onPodDelete(obj any) {
	switch t := obj.(type) {
	case *corev1.Pod:
		c.del(t.Status.PodIP, t.Name)
	case k8scache.DeletedFinalStateUnknown:
		if pod, ok := t.Obj.(*corev1.Pod); ok {
			c.del(pod.Status.PodIP, pod.Name)
		}
	}
}

// routerInstallNamespace returns the namespace the router runs in (the Fission
// install namespace), used to exempt internal components from the guard. Reads
// POD_NAMESPACE (set by the chart via the downward API), falling back to the
// service account namespace file that is always mounted into a pod.
func routerInstallNamespace() string {
	if ns := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); ns != "" {
		return ns
	}
	if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(data)); ns != "" {
			return ns
		}
	}
	return ""
}
