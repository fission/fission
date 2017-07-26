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

package tpr

import (
	"k8s.io/client-go/1.5/pkg/api"
	"k8s.io/client-go/1.5/pkg/api/meta"
	"k8s.io/client-go/1.5/pkg/api/unversioned"

	"github.com/fission/fission"
)

//
// To add a Fission TPR type:
//   1. Create a "spec" type, for everything in the type except metadata
//   2. Create the type with metadata + the spec
//   3. Create a list type (for example see FunctionList and Function, below)
//   4. Add methods at the bottom of this file for satisfying Object and List interfaces
//   5. Add the type to configureClient in client.go
//   6. Add the type to EnsureFissionTPRs in tpr.go
//   7. Add tests to tpr_test.go
//

type (
	// Functions.
	Function struct {
		unversioned.TypeMeta `json:",inline"`
		Metadata             api.ObjectMeta       `json:"metadata"`
		Spec                 fission.FunctionSpec `json:"spec"`
	}
	FunctionList struct {
		unversioned.TypeMeta `json:",inline"`
		Metadata             unversioned.ListMeta `json:"metadata"`

		Items []Function `json:"items"`
	}

	// Environments.
	Environment struct {
		unversioned.TypeMeta `json:",inline"`
		Metadata             api.ObjectMeta          `json:"metadata"`
		Spec                 fission.EnvironmentSpec `json:"spec"`
	}
	EnvironmentList struct {
		unversioned.TypeMeta `json:",inline"`
		Metadata             unversioned.ListMeta `json:"metadata"`

		Items []Environment `json:"items"`
	}

	// HTTP Triggers.  (Something in the TPR reflection stuff wants
	// it to be spelled "Httptrigger" not "HTTPTrigger" or even
	// "HttpTrigger".  Bleh.)
	Httptrigger struct {
		unversioned.TypeMeta `json:",inline"`
		Metadata             api.ObjectMeta          `json:"metadata"`
		Spec                 fission.HTTPTriggerSpec `json:"spec"`
	}
	HttptriggerList struct {
		unversioned.TypeMeta `json:",inline"`
		Metadata             unversioned.ListMeta `json:"metadata"`

		Items []Httptrigger `json:"items"`
	}

	// Kubernetes Watches as triggers
	Kuberneteswatchtrigger struct {
		unversioned.TypeMeta `json:",inline"`
		Metadata             api.ObjectMeta                     `json:"metadata"`
		Spec                 fission.KubernetesWatchTriggerSpec `json:"spec"`
	}
	KuberneteswatchtriggerList struct {
		unversioned.TypeMeta `json:",inline"`
		Metadata             unversioned.ListMeta `json:"metadata"`

		Items []Kuberneteswatchtrigger `json:"items"`
	}

	// Time triggers
	Timetrigger struct {
		unversioned.TypeMeta `json:",inline"`
		Metadata             api.ObjectMeta          `json:"metadata"`
		Spec                 fission.TimeTriggerSpec `json:"spec"`
	}
	TimetriggerList struct {
		unversioned.TypeMeta `json:",inline"`
		Metadata             unversioned.ListMeta `json:"metadata"`

		Items []Timetrigger `json:"items"`
	}

	// Message Queue triggers
	Messagequeuetrigger struct {
		unversioned.TypeMeta `json:",inline"`
		Metadata             api.ObjectMeta                  `json:"metadata"`
		Spec                 fission.MessageQueueTriggerSpec `json:"spec"`
	}
	MessagequeuetriggerList struct {
		unversioned.TypeMeta `json:",inline"`
		Metadata             unversioned.ListMeta `json:"metadata"`

		Items []Messagequeuetrigger `json:"items"`
	}
)

// Each TPR type needs:
//   GetObjectKind (to satisfy the Object interface)
//
// In addition, each singular TPR type needs:
//   GetObjectMeta (to satisfy the ObjectMetaAccessor interface)
//
// And each list TPR type needs:
//   GetListMeta (to satisfy the ListMetaAccessor interface)

func (f *Function) GetObjectKind() unversioned.ObjectKind {
	return &f.TypeMeta
}
func (e *Environment) GetObjectKind() unversioned.ObjectKind {
	return &e.TypeMeta
}
func (ht *Httptrigger) GetObjectKind() unversioned.ObjectKind {
	return &ht.TypeMeta
}
func (w *Kuberneteswatchtrigger) GetObjectKind() unversioned.ObjectKind {
	return &w.TypeMeta
}
func (w *Timetrigger) GetObjectKind() unversioned.ObjectKind {
	return &w.TypeMeta
}
func (w *Messagequeuetrigger) GetObjectKind() unversioned.ObjectKind {
	return &w.TypeMeta
}

func (f *Function) GetObjectMeta() meta.Object {
	return &f.Metadata
}
func (e *Environment) GetObjectMeta() meta.Object {
	return &e.Metadata
}
func (ht *Httptrigger) GetObjectMeta() meta.Object {
	return &ht.Metadata
}
func (w *Kuberneteswatchtrigger) GetObjectMeta() meta.Object {
	return &w.Metadata
}
func (w *Timetrigger) GetObjectMeta() meta.Object {
	return &w.Metadata
}
func (w *Messagequeuetrigger) GetObjectMeta() meta.Object {
	return &w.Metadata
}

func (fl *FunctionList) GetObjectKind() unversioned.ObjectKind {
	return &fl.TypeMeta
}
func (el *EnvironmentList) GetObjectKind() unversioned.ObjectKind {
	return &el.TypeMeta
}
func (hl *HttptriggerList) GetObjectKind() unversioned.ObjectKind {
	return &hl.TypeMeta
}
func (wl *KuberneteswatchtriggerList) GetObjectKind() unversioned.ObjectKind {
	return &wl.TypeMeta
}
func (wl *TimetriggerList) GetObjectKind() unversioned.ObjectKind {
	return &wl.TypeMeta
}
func (wl *MessagequeuetriggerList) GetObjectKind() unversioned.ObjectKind {
	return &wl.TypeMeta
}

func (fl *FunctionList) GetListMeta() unversioned.List {
	return &fl.Metadata
}
func (el *EnvironmentList) GetListMeta() unversioned.List {
	return &el.Metadata
}
func (hl *HttptriggerList) GetListMeta() unversioned.List {
	return &hl.Metadata
}
func (wl *KuberneteswatchtriggerList) GetListMeta() unversioned.List {
	return &wl.Metadata
}
func (wl *TimetriggerList) GetListMeta() unversioned.List {
	return &wl.Metadata
}
func (wl *MessagequeuetriggerList) GetListMeta() unversioned.List {
	return &wl.Metadata
}

// In the client-go TPR example, UnmarshalJSON is defined here for the
// singular and list types.  That's supposed to be a workaround for
// some ugorji bug.  But we don't seem to need it, and all our tests
// pass without it, so we don't define any UnmarshalJSON methods.
