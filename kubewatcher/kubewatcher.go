/*
Copyright 2016 The Fission Authors.

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

package kubewatcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/pkg/api"
	apierrs "k8s.io/client-go/1.5/pkg/api/errors"
	"k8s.io/client-go/1.5/pkg/api/meta"
	"k8s.io/client-go/1.5/pkg/runtime"
	"k8s.io/client-go/1.5/pkg/watch"

	"github.com/fission/fission"
	"github.com/fission/fission/publisher"
	"net/http"
	"reflect"
)

type requestType int

const (
	SYNC requestType = iota
)

type (
	KubeWatcher struct {
		watches          map[string]watchSubscription
		kubernetesClient *kubernetes.Clientset
		requestChannel   chan *kubeWatcherRequest
		publisher        publisher.Publisher
		routerUrl        string
	}

	watchSubscription struct {
		fission.Watch
		kubeWatch           watch.Interface
		lastResourceVersion string
		stopped             *int32
		kubernetesClient    *kubernetes.Clientset
		publisher           publisher.Publisher
		routerUrl           string
	}

	kubeWatcherRequest struct {
		requestType
		watches         []fission.Watch
		responseChannel chan *kubeWatcherResponse
	}
	kubeWatcherResponse struct {
		error
	}
)

func MakeKubeWatcher(kubernetesClient *kubernetes.Clientset, publisher publisher.Publisher, routerUrl string) *KubeWatcher {
	kw := &KubeWatcher{
		watches:          make(map[string]watchSubscription),
		kubernetesClient: kubernetesClient,
		publisher:        publisher,
		requestChannel:   make(chan *kubeWatcherRequest),
		routerUrl:        routerUrl,
	}
	go kw.svc()
	return kw
}

func (kw *KubeWatcher) Sync(watches []fission.Watch) error {
	req := &kubeWatcherRequest{
		requestType:     SYNC,
		watches:         watches,
		responseChannel: make(chan *kubeWatcherResponse),
	}
	kw.requestChannel <- req
	resp := <-req.responseChannel
	return resp.error
}

func (kw *KubeWatcher) svc() {
	for {
		req := <-kw.requestChannel
		switch req.requestType {
		case SYNC:
			newWatchUids := make(map[string]bool)
			for _, w := range req.watches {
				newWatchUids[w.Metadata.Uid] = true
			}
			// Remove old watches
			for uid, ws := range kw.watches {
				if _, ok := newWatchUids[uid]; !ok {
					kw.removeWatch(&ws.Watch)
				}
			}
			// Add new watches
			for _, w := range req.watches {
				if _, ok := kw.watches[w.Metadata.Uid]; !ok {
					kw.addWatch(&w)
				}
			}
			req.responseChannel <- &kubeWatcherResponse{error: nil}
		}
	}
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

func createKubernetesWatch(kubeClient *kubernetes.Clientset, w *fission.Watch, resourceVersion string) (watch.Interface, error) {
	var wi watch.Interface
	var err error
	var watchTimeoutSec int64 = 120

	// TODO populate labelselector and fieldselector
	listOptions := api.ListOptions{
		ResourceVersion: resourceVersion,
		TimeoutSeconds:  &watchTimeoutSec,
	}

	// TODO handle the full list of types
	switch strings.ToUpper(w.ObjType) {
	case "POD":
		wi, err = kubeClient.Core().Pods(w.Namespace).Watch(listOptions)
	case "SERVICE":
		wi, err = kubeClient.Core().Services(w.Namespace).Watch(listOptions)
	default:
		msg := fmt.Sprintf("Error: unknown obj type '%v'", w.ObjType)
		log.Println(msg)
		err = errors.New(msg)
	}
	return wi, err
}

func (kw *KubeWatcher) addWatch(w *fission.Watch) error {
	log.Printf("Adding watch %v: %v", w.Metadata.Name, w.Function.Name)
	ws, err := MakeWatchSubscription(w, kw.kubernetesClient, kw.publisher, kw.routerUrl)
	if err != nil {
		return err
	}
	kw.watches[w.Metadata.Uid] = *ws
	return nil
}

func (kw *KubeWatcher) removeWatch(w *fission.Watch) error {
	log.Printf("Removing watch %v: %v", w.Metadata.Name, w.Function.Name)
	ws, ok := kw.watches[w.Metadata.Uid]
	if !ok {
		return fission.MakeError(fission.ErrorNotFound,
			fmt.Sprintf("watch doesn't exist: %v", w.Metadata))
	}
	delete(kw.watches, w.Metadata.Uid)
	atomic.StoreInt32(ws.stopped, 1)
	ws.kubeWatch.Stop()
	return nil
}

// 	wi, err := kw.createKubernetesWatch(w)
// 	if err != nil {
// 		return err
// 	}
// 	var stopped int32 = 0
// 	ws := &watchSubscription{
// 		Watch:     *w,
// 		kubeWatch: wi,
// 		stopped:   &stopped,
// 	}
// 	kw.watches[w.Metadata.Uid] = *ws
// 	go ws.eventDispatchLoop(kw.publisher)
// 	return nil
// }

func MakeWatchSubscription(w *fission.Watch, kubeClient *kubernetes.Clientset, publisher publisher.Publisher, routerUrl string) (*watchSubscription, error) {
	var stopped int32 = 0
	ws := &watchSubscription{
		Watch:               *w,
		kubeWatch:           nil,
		stopped:             &stopped,
		kubernetesClient:    kubeClient,
		publisher:           publisher,
		lastResourceVersion: "",
		routerUrl:           routerUrl,
	}

	err := ws.restartWatch()
	if err != nil {
		return nil, err
	}

	go ws.eventDispatchLoop()
	return ws, nil
}

func (ws *watchSubscription) restartWatch() error {
	retries := 60
	for {
		log.Printf("(re)starting watch %v (ns:%v type:%v) at rv:%v",
			ws.Watch.Metadata, ws.Watch.Namespace, ws.Watch.ObjType, ws.lastResourceVersion)
		wi, err := createKubernetesWatch(ws.kubernetesClient, &ws.Watch, ws.lastResourceVersion)
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
	meta, err := meta.Accessor(obj)
	if err != nil {
		return "", err
	}
	return meta.GetResourceVersion(), nil
}

func (ws *watchSubscription) eventDispatchLoop() {
	log.Println("Listening to watch ", ws.Watch.Metadata.Name)
	for {
		for {
			ev, more := <-ws.kubeWatch.ResultChan()
			if !more {
				log.Println("Watch stopped", ws.Watch.Metadata.Name)
				break
			}
			if ev.Type == watch.Error {
				e := apierrs.FromObject(ev.Object)
				log.Println("Watch error, retrying in a second: %v", e)
				// Start from the beginning to get around "too old resource version"
				ws.lastResourceVersion = ""
				time.Sleep(time.Second)
				break
			}
			rv, err := getResourceVersion(ev.Object)
			if err != nil {
				log.Printf("Error getting resourceVersion from object: %v", err)
			} else {
				log.Printf("rv=%v", rv)
				ws.lastResourceVersion = rv
			}

			ws.publisher.Publish(ws.newHttpRequest(ev, ws.Watch.Target))
		}
		if atomic.LoadInt32(ws.stopped) == 0 {
			err := ws.restartWatch()
			if err != nil {
				log.Panicf("Failed to restart watch: %v", err)
			}
		}
	}
}

func (ws *watchSubscription) newHttpRequest(ev watch.Event, target string) *http.Request {
	url := ws.routerUrl + "/" + strings.TrimPrefix(target, "/")
	// Serialize the object
	var buf bytes.Buffer
	err := printKubernetesObject(ev.Object, &buf)
	if err != nil {
		log.Printf("Failed to serialize object: %v", err)
		// TODO send a POST request indicating error
	}

	// Create request
	req, err := http.NewRequest("POST", url, &buf)
	if err != nil {
		log.Printf("Failed to create request to %v", url)
		// can't do anything more, drop the event.
		return nil
	}

	// Event and object type aren't in the serialized object
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("X-Kubernetes-Event-Type", string(ev.Type))
	req.Header.Add("X-Kubernetes-Object-Type", reflect.TypeOf(ev.Object).Elem().Name())
	return req
}
