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
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
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
	client  cmd.Client
	crdType string
}

func NewCrdDumper(client cmd.Client, crdType string) Resource {
	return CrdDumper{client: client, crdType: crdType}
}

func (res CrdDumper) Dump(ctx context.Context, dumpDir string) {

	switch res.crdType {
	case CrdEnvironment:
		items, err := res.client.FissionClientSet.CoreV1().Environments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			console.Warn(fmt.Sprintf("Error getting %v list: %v", res.crdType, err))
			return
		}

		for _, item := range items.Items {
			f := getFileName(dumpDir, item.ObjectMeta)
			writeToFile(f, item)
		}

	case CrdFunction:
		items, err := res.client.FissionClientSet.CoreV1().Functions(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			console.Warn(fmt.Sprintf("Error getting %v list: %v", res.crdType, err))
			return
		}

		for _, item := range items.Items {
			f := getFileName(dumpDir, item.ObjectMeta)
			writeToFile(f, item)
		}

	case CrdPackage:
		items, err := res.client.FissionClientSet.CoreV1().Packages(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			console.Warn(fmt.Sprintf("Error getting %v list: %v", res.crdType, err))
			return
		}

		for _, item := range items.Items {
			item = pkgClean(item)
			f := getFileName(dumpDir, item.ObjectMeta)
			writeToFile(f, item)
		}

	case CrdHttpTrigger:
		items, err := res.client.FissionClientSet.CoreV1().HTTPTriggers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			console.Warn(fmt.Sprintf("Error getting %v list: %v", res.crdType, err))
			return
		}

		for _, item := range items.Items {
			f := getFileName(dumpDir, item.ObjectMeta)
			writeToFile(f, item)
		}

	case CrdKubeWatcher:
		items, err := res.client.FissionClientSet.CoreV1().KubernetesWatchTriggers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			console.Warn(fmt.Sprintf("Error getting %v list: %v", res.crdType, err))
			return
		}

		for _, item := range items.Items {
			f := getFileName(dumpDir, item.ObjectMeta)
			writeToFile(f, item)
		}

	case CrdMessageQueueTrigger:
		var triggers []fv1.MessageQueueTrigger

		l, err := res.client.FissionClientSet.CoreV1().MessageQueueTriggers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			console.Warn(fmt.Sprintf("Error getting %v list: %v", res.crdType, err))
			break
		}
		triggers = append(triggers, l.Items...)

		for _, item := range triggers {
			f := getFileName(dumpDir, item.ObjectMeta)
			writeToFile(f, item)
		}

	case CrdTimeTrigger:
		items, err := res.client.FissionClientSet.CoreV1().TimeTriggers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			console.Warn(fmt.Sprintf("Error getting %v list: %v", res.crdType, err))
			return
		}

		for _, item := range items.Items {
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
