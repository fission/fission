/*
Copyright 2018 The Fission Authors.

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

package resources

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/console"
)

const (
	CrdEnvironment = "Environment"
	CrdFunction    = "Function"
	CrdPackage     = "Packages"

	CrdHttpTrigger         = "HTTPTrigger"
	CrdKubeWatcher         = "KubeWatcher"
	CrdMessageQueueTrigger = "MessageQueue"
	CrdTimeTrigger         = "TimeTrigger"
)

type CrdDumper struct {
	client  client.Interface
	crdType string
}

func NewCrdDumper(client client.Interface, crdType string) Resource {
	return CrdDumper{client: client, crdType: crdType}
}

func (res CrdDumper) Dump(dumpDir string) {

	switch res.crdType {
	case CrdEnvironment:
		items, err := res.client.V1().Environment().List(metav1.NamespaceAll)
		if err != nil {
			console.Warn(fmt.Sprintf("Error getting %v list: %v", res.crdType, err))
			return
		}

		for _, item := range items {
			f := getFileName(dumpDir, item.ObjectMeta)
			writeToFile(f, item)
		}

	case CrdFunction:
		items, err := res.client.V1().Function().List(metav1.NamespaceAll)
		if err != nil {
			console.Warn(fmt.Sprintf("Error getting %v list: %v", res.crdType, err))
			return
		}

		for _, item := range items {
			f := getFileName(dumpDir, item.ObjectMeta)
			writeToFile(f, item)
		}

	case CrdPackage:
		items, err := res.client.V1().Package().List(metav1.NamespaceAll)
		if err != nil {
			console.Warn(fmt.Sprintf("Error getting %v list: %v", res.crdType, err))
			return
		}

		for _, item := range items {
			item = pkgClean(item)
			f := getFileName(dumpDir, item.ObjectMeta)
			writeToFile(f, item)
		}

	case CrdHttpTrigger:
		items, err := res.client.V1().HTTPTrigger().List(metav1.NamespaceAll)
		if err != nil {
			console.Warn(fmt.Sprintf("Error getting %v list: %v", res.crdType, err))
			return
		}

		for _, item := range items {
			f := getFileName(dumpDir, item.ObjectMeta)
			writeToFile(f, item)
		}

	case CrdKubeWatcher:
		items, err := res.client.V1().KubeWatcher().List(metav1.NamespaceAll)
		if err != nil {
			console.Warn(fmt.Sprintf("Error getting %v list: %v", res.crdType, err))
			return
		}

		for _, item := range items {
			f := getFileName(dumpDir, item.ObjectMeta)
			writeToFile(f, item)
		}

	case CrdMessageQueueTrigger:
		var triggers []fv1.MessageQueueTrigger

		for _, mqType := range []string{fv1.MessageQueueTypeKafka} {
			l, err := res.client.V1().MessageQueueTrigger().List(mqType, metav1.NamespaceAll)
			if err != nil {
				console.Warn(fmt.Sprintf("Error getting %v list: %v", res.crdType, err))
				break
			}
			triggers = append(triggers, l...)
		}

		for _, item := range triggers {
			f := getFileName(dumpDir, item.ObjectMeta)
			writeToFile(f, item)
		}

	case CrdTimeTrigger:
		items, err := res.client.V1().TimeTrigger().List(metav1.NamespaceAll)
		if err != nil {
			console.Warn(fmt.Sprintf("Error getting %v list: %v", res.crdType, err))
			return
		}

		for _, item := range items {
			f := getFileName(dumpDir, item.ObjectMeta)
			writeToFile(f, item)
		}

	default:
		console.Warn(fmt.Sprintf("Unknown type: %v", res.crdType))
	}
}

func pkgClean(pkg fv1.Package) fv1.Package {
	// mask the sensitive information
	// use "-" as mask value to indicate the field wasn't empty
	if pkg.Spec.Source.Literal != nil {
		pkg.Spec.Source.Literal = []byte("-")
	}
	if pkg.Spec.Deployment.Literal != nil {
		pkg.Spec.Deployment.Literal = []byte("-")
	}
	return pkg
}
