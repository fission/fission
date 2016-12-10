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

	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/pkg/api"
	"k8s.io/client-go/1.5/pkg/runtime"
	"k8s.io/client-go/1.5/pkg/watch"

	"github.com/platform9/fission"
)

type requestType int

const (
	SYNC requestType = iota
)

type (
	watchSubscription struct {
		fission.Watch
		kubeWatch watch.Interface
	}

	kubeWatcherRequest struct {
		requestType
		watches         []fission.Watch
		responseChannel chan *kubeWatcherResponse
	}
	kubeWatcherResponse struct {
		error
	}

	KubeWatcher struct {
		watches          map[string]watchSubscription
		kubernetesClient *kubernetes.Clientset
		poster           *Poster
		requestChannel   chan *kubeWatcherRequest
	}
)

func MakeKubeWatcher(kubernetesClient *kubernetes.Clientset, poster *Poster) *KubeWatcher {
	kw := &KubeWatcher{
		watches:          make(map[string]watchSubscription),
		kubernetesClient: kubernetesClient,
		poster:           poster,
		requestChannel:   make(chan *kubeWatcherRequest),
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

// lifted from kubernetes/pkg/kubectl/resource_printer.go
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

func (kw *KubeWatcher) createKubernetesWatch(w *fission.Watch) (watch.Interface, error) {
	var wi watch.Interface
	var err error

	listOptions := api.ListOptions{} // TODO populate labelselector and fieldselector

	// TODO handle the full list of types
	switch strings.ToUpper(w.ObjType) {
	case "POD":
		wi, err = kw.kubernetesClient.Core().Pods(w.Namespace).Watch(listOptions)
	case "SERVICE":
		wi, err = kw.kubernetesClient.Core().Services(w.Namespace).Watch(listOptions)
	default:
		msg := fmt.Sprintf("Error: unknown obj type '%v'", w.ObjType)
		log.Println(msg)
		err = errors.New(msg)
	}
	return wi, err
}

func (kw *KubeWatcher) addWatch(w *fission.Watch) error {
	log.Printf("Adding watch %v: %v", w.Metadata.Name, w.Function.Name)
	wi, err := kw.createKubernetesWatch(w)
	if err != nil {
		return err
	}
	ws := &watchSubscription{
		Watch:     *w,
		kubeWatch: wi,
	}
	kw.watches[w.Metadata.Uid] = *ws
	go ws.eventDispatchLoop(kw.poster)
	return nil
}

func (kw *KubeWatcher) removeWatch(w *fission.Watch) error {
	log.Printf("Removing watch %v: %v", w.Metadata.Name, w.Function.Name)
	ws, ok := kw.watches[w.Metadata.Uid]
	if !ok {
		return fission.MakeError(fission.ErrorNotFound,
			fmt.Sprintf("watch doesn't exist: %v", w.Metadata))
	}
	ws.kubeWatch.Stop()
	delete(kw.watches, w.Metadata.Uid)
	return nil
}

func (ws *watchSubscription) eventDispatchLoop(poster *Poster) {
	log.Println("Listening to watch ", ws.Watch.Metadata.Name)
	for {
		ev, more := <-ws.kubeWatch.ResultChan()
		if !more {
			log.Println("Watch stopped", ws.Watch.Metadata.Name)
			return
		}

		var buf bytes.Buffer
		err := printKubernetesObject(ev.Object, &buf)
		if err != nil {
			log.Println("Failed to serialize object: %v", err)
		}
		poster.Post(string(ev.Type), ws.Watch.Url, &buf)
	}
}
