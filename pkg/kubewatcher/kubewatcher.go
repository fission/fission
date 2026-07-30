// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package kubewatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/publisher"
	"github.com/fission/fission/pkg/utils"
)

type (
	KubeWatcher struct {
		logger           logr.Logger
		watches          map[types.NamespacedName]*watchSubscription
		kubernetesClient kubernetes.Interface
		publisher        publisher.Publisher
	}

	watchSubscription struct {
		logger              logr.Logger
		watch               fv1.KubernetesWatchTrigger
		kubeWatch           watch.Interface
		lastResourceVersion string
		stopped             atomic.Int32
		kubernetesClient    kubernetes.Interface
		publisher           publisher.Publisher
	}
)

func MakeKubeWatcher(ctx context.Context, logger logr.Logger, kubernetesClient kubernetes.Interface, publisher publisher.Publisher) *KubeWatcher {
	kw := &KubeWatcher{
		logger:           logger.WithName("kube_watcher"),
		watches:          make(map[types.NamespacedName]*watchSubscription),
		kubernetesClient: kubernetesClient,
		publisher:        publisher,
	}
	return kw
}

// TODO lifted from kubernetes/pkg/kubectl/resource_printer.go.
func printKubernetesObject(obj runtime.Object, w io.Writer) error {
	switch obj := obj.(type) {
	case *runtime.Unknown:
		var buf bytes.Buffer
		err := json.Indent(&buf, obj.Raw, "", "    ")
		if err != nil {
			return err
		}
		buf.WriteRune('\n')
		_, err = buf.WriteTo(w)
		return err
	}

	data, err := json.MarshalIndent(obj, "", "    ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

func createKubernetesWatch(ctx context.Context, kubeClient kubernetes.Interface, w *fv1.KubernetesWatchTrigger, resourceVersion string) (watch.Interface, error) {
	var wi watch.Interface
	var err error
	var watchTimeoutSec int64 = 120

	// Refuse cross-namespace targets — the webhook should already reject
	// these at admission, but reconcile loops can see stale objects on
	// upgraded clusters or on webhook-failurePolicy=Ignore deployments
	// (GHSA-gc3j-79f2-7vvw).
	if w.Spec.Namespace != "" && w.Spec.Namespace != w.Namespace {
		return nil, fmt.Errorf("cross-namespace watch is not allowed: trigger.namespace=%s spec.namespace=%s",
			w.Namespace, w.Spec.Namespace)
	}

	// An empty Spec.Namespace previously meant "watch all namespaces" via
	// client-go's empty-namespace semantics — a separate cross-tenant leak.
	// Coerce it to the trigger's own namespace so an unset field can never
	// resolve to cluster-wide visibility.
	target := w.Spec.Namespace
	if target == "" {
		target = w.Namespace
	}

	// TODO populate labelselector and fieldselector
	listOptions := metav1.ListOptions{
		ResourceVersion: resourceVersion,
		TimeoutSeconds:  &watchTimeoutSec,
	}

	// TODO handle the full list of types
	switch strings.ToUpper(w.Spec.Type) {
	case "POD":
		wi, err = kubeClient.CoreV1().Pods(target).Watch(ctx, listOptions)
	case "SERVICE":
		wi, err = kubeClient.CoreV1().Services(target).Watch(ctx, listOptions)
	case "REPLICATIONCONTROLLER":
		wi, err = kubeClient.CoreV1().ReplicationControllers(target).Watch(ctx, listOptions)
	case "JOB":
		wi, err = kubeClient.BatchV1().Jobs(target).Watch(ctx, listOptions)
	default:
		err = errors.NewBadRequest(fmt.Sprintf("Error: unknown obj type '%v'", w.Spec.Type))
	}
	return wi, err
}

// addWatch (re)starts the watch subscription for a trigger. An existing
// subscription for the same trigger is stopped before being replaced so a
// re-reconcile (e.g. a spec change or a retried failure) can't leak the old
// watch goroutine. Keyed by namespaced name so the reconciler can tear it down
// on a delete, when only the name is known. Status conditions are written by
// the reconciler via the shared helper, not here.
func (kw *KubeWatcher) addWatch(ctx context.Context, w *fv1.KubernetesWatchTrigger) error {
	kw.logger.Info("adding watch", "name", w.Name, "function", w.Spec.FunctionReference)
	key := types.NamespacedName{Namespace: w.Namespace, Name: w.Name}

	// Stop and drop any existing subscription before starting the replacement.
	// Doing this first means a failed (re)start — e.g. a spec change to an
	// invalid watch type — leaves no stale watch firing for the superseded
	// config; the trigger is simply marked not-Ready and the reconcile is
	// retried. (A transient start failure on an unchanged spec re-creates the
	// watch on the next requeue.)
	if old, ok := kw.watches[key]; ok {
		old.stop()
		delete(kw.watches, key)
	}

	ws, err := MakeWatchSubscription(ctx, kw.logger.WithName("watchsubscription"), w, kw.kubernetesClient, kw.publisher)
	if err != nil {
		return err
	}
	kw.watches[key] = ws
	return nil
}

// removeWatch stops and drops the watch subscription for a deleted trigger.
// No-op if the trigger was never watched (e.g. a delete observed before any
// add).
func (kw *KubeWatcher) removeWatch(key types.NamespacedName) {
	ws, ok := kw.watches[key]
	if !ok {
		return
	}
	kw.logger.Info("removing watch", "name", key.Name, "namespace", key.Namespace)
	delete(kw.watches, key)
	ws.stop()
}

func MakeWatchSubscription(ctx context.Context, logger logr.Logger, w *fv1.KubernetesWatchTrigger, kubeClient kubernetes.Interface, publisher publisher.Publisher) (*watchSubscription, error) {

	ws := &watchSubscription{
		logger:              logger.WithName("watch_subscription"),
		watch:               *w,
		kubeWatch:           nil,
		kubernetesClient:    kubeClient,
		publisher:           publisher,
		lastResourceVersion: "",
	}

	err := ws.restartWatch(ctx)
	if err != nil {
		return nil, err
	}

	go ws.eventDispatchLoop(ctx)
	return ws, nil
}

// watchStartRetries bounds how many times restartWatch retries establishing
// the watch before giving up. Overridable in tests to avoid the multi-second
// retry loop on a deliberately-failing start.
var watchStartRetries = 60

func (ws *watchSubscription) restartWatch(ctx context.Context) error {
	retries := watchStartRetries
	for {
		ws.logger.Info("(re)starting watch",
			"watch", ws.watch.ObjectMeta,
			"namespace", ws.watch.Spec.Namespace,
			"type", ws.watch.Spec.Type,
			"last_resource_version", ws.lastResourceVersion)
		wi, err := createKubernetesWatch(ctx, ws.kubernetesClient, &ws.watch, ws.lastResourceVersion)
		if err != nil {
			retries--
			if retries > 0 {
				time.Sleep(500 * time.Millisecond)
				continue
			} else {
				return err
			}
		}
		ws.kubeWatch = wi
		return nil
	}
}

func getResourceVersion(obj runtime.Object) (string, error) {
	m, err := meta.Accessor(obj)
	if err != nil {
		return "", err
	}
	return m.GetResourceVersion(), nil
}

func (ws *watchSubscription) eventDispatchLoop(ctx context.Context) {
	ws.logger.Info("listening to watch", "name", ws.watch.Name)
	// check watchSubscription is stopped or not before waiting for event
	// comes from the kubeWatch.ResultChan(). This fix the edge case that
	// new kubewatch is created in the restartWatch() while the old kubewatch
	// is being used in watchSubscription.stop().
	for !ws.isStopped() {
		ev, more := <-ws.kubeWatch.ResultChan()
		if !more {
			if ws.isStopped() {
				// watch is removed by user.
				ws.logger.Info("watch stopped", "watch_name", ws.watch.Name)
				return
			} else {
				// watch closed due to timeout, restart it.
				ws.logger.Info("watch timed out - restarting", "watch_name", ws.watch.Name)
				err := ws.restartWatch(ctx)
				if err != nil {
					ws.logger.Error(err, "failed to restart watch", "watch_name", ws.watch.Name)
					os.Exit(1)
				}
				continue
			}
		}

		if ev.Type == watch.Error {
			e := errors.FromObject(ev.Object)
			ws.logger.Error(e, "watch error - retrying after one second", "watch_name", ws.watch.Name)
			// Start from the beginning to get around "too old resource version"
			ws.lastResourceVersion = ""
			time.Sleep(time.Second)
			err := ws.restartWatch(ctx)
			if err != nil {
				ws.logger.Error(err, "failed to restart watch", "watch_name", ws.watch.Name)
				os.Exit(1)
			}
			continue
		}
		rv, err := getResourceVersion(ev.Object)
		if err != nil {
			ws.logger.Error(err, "error getting resourceVersion from object", "watch_name", ws.watch.Name)
		} else {
			ws.lastResourceVersion = rv
		}

		// Serialize the object
		var buf bytes.Buffer
		err = printKubernetesObject(ev.Object, &buf)
		if err != nil {
			ws.logger.Error(err, "failed to serialize object", "watch_name", ws.watch.Name)
			// TODO send a POST request indicating error
		}

		// Event and object type aren't in the serialized object
		headers := map[string]string{
			"Content-Type":             "application/json",
			"X-Kubernetes-Event-Type":  string(ev.Type),
			"X-Kubernetes-Object-Type": reflect.TypeOf(ev.Object).Elem().Name(),
		}

		// TODO support other function ref types. Or perhaps delegate to router?
		if ws.watch.Spec.FunctionReference.Type != fv1.FunctionReferenceTypeFunctionName {
			ws.logger.Error(nil, "unsupported function ref type - cannot publish event", "type", ws.watch.Spec.FunctionReference.Type,
				"watch_name", ws.watch.Name)
			continue
		}

		// with the addition of multi-tenancy, the users can create functions in any namespace. however,
		// the triggers can only be created in the same namespace as the function.
		// so essentially, function namespace = trigger namespace.
		// RFC-0025: append the alias/version suffix when the reference carries
		// one; resolution stays entirely router-side.
		url := utils.UrlForFunctionReference(ws.watch.Spec.FunctionReference, ws.watch.Namespace)
		ws.publisher.Publish(ctx, buf.String(), headers, http.MethodPost, url)
	}
}

func (ws *watchSubscription) stop() {
	ws.stopped.Store(1)
	ws.kubeWatch.Stop()
}

func (ws *watchSubscription) isStopped() bool {
	return ws.stopped.Load() == 1
}
