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

package poolmgr

import (
	"log"
	"time"

	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// cleanupOldPoolmgrResources looks for resources created by an old
// poolmgr instance and cleans them up.
func cleanupOldPoolmgrResources(client *kubernetes.Clientset, namespace string, instanceId string) {
	go func() {
		err := cleanup(client, namespace, instanceId)
		if err != nil {
			// TODO retry cleanup; logged and ignored for now
			log.Printf("Failed to cleanup: %v", err)
		}
	}()
}

func cleanup(client *kubernetes.Clientset, namespace string, instanceId string) error {
	// Deployments are used for idle pools and can be cleaned up
	// immediately.  (We should "adopt" these instead of creating
	// a new pool.)
	err := cleanupDeployments(client, namespace, instanceId)
	if err != nil {
		return err
	}
	// See K8s #33845 and related bugs: deleting a deployment
	// through the API doesn't cause the associated ReplicaSet to
	// be deleted.  (Fixed recently, but we may be running a
	// version before the fix.)
	err = cleanupReplicaSets(client, namespace, instanceId)
	if err != nil {
		return err
	}

	// Pods might still be running user functions, so we give them
	// a few minutes before terminating them.  This time is the
	// maximum function runtime, plus the time a router might
	// still route to an old instance, i.e. router cache expiry
	// time.
	time.Sleep(6 * time.Minute)

	err = cleanupPods(client, namespace, instanceId)
	if err != nil {
		return err
	}

	err = cleanupServices(client, namespace, instanceId)
	if err != nil {
		return err
	}

	return nil
}

func cleanupDeployments(client *kubernetes.Clientset, namespace string, instanceId string) error {
	deploymentList, err := client.ExtensionsV1beta1().Deployments(namespace).List(meta_v1.ListOptions{})
	if err != nil {
		return err
	}
	for _, dep := range deploymentList.Items {
		id, ok := dep.ObjectMeta.Labels[POOLMGR_INSTANCEID_LABEL]
		if ok && id != instanceId {
			log.Printf("Cleaning up deployment %v", dep.ObjectMeta.Name)
			err := client.ExtensionsV1beta1().Deployments(namespace).Delete(dep.ObjectMeta.Name, nil)
			logErr("cleaning up deployment", err)
			// ignore err
		}
	}
	return nil
}

func cleanupReplicaSets(client *kubernetes.Clientset, namespace string, instanceId string) error {
	rsList, err := client.ExtensionsV1beta1().ReplicaSets(namespace).List(meta_v1.ListOptions{})
	if err != nil {
		return err
	}
	for _, rs := range rsList.Items {
		id, ok := rs.ObjectMeta.Labels[POOLMGR_INSTANCEID_LABEL]
		if ok && id != instanceId {
			log.Printf("Cleaning up replicaset %v", rs.ObjectMeta.Name)
			err := client.ExtensionsV1beta1().ReplicaSets(namespace).Delete(rs.ObjectMeta.Name, nil)
			logErr("cleaning up replicaset", err)
		}
	}
	return nil
}

func cleanupPods(client *kubernetes.Clientset, namespace string, instanceId string) error {
	podList, err := client.CoreV1().Pods(namespace).List(meta_v1.ListOptions{})
	if err != nil {
		return err
	}
	for _, pod := range podList.Items {
		id, ok := pod.ObjectMeta.Labels[POOLMGR_INSTANCEID_LABEL]
		if ok && id != instanceId {
			log.Printf("Cleaning up pod %v", pod.ObjectMeta.Name)
			err := client.CoreV1().Pods(namespace).Delete(pod.ObjectMeta.Name, nil)
			logErr("cleaning up pod", err)
			// ignore err
		}
	}
	return nil
}

func cleanupServices(client *kubernetes.Clientset, namespace string, instanceId string) error {
	svcList, err := client.CoreV1().Services(namespace).List(meta_v1.ListOptions{})
	if err != nil {
		return err
	}
	for _, svc := range svcList.Items {
		id, ok := svc.ObjectMeta.Labels[POOLMGR_INSTANCEID_LABEL]
		if ok && id != instanceId {
			log.Printf("Cleaning up svc %v", svc.ObjectMeta.Name)
			err := client.CoreV1().Services(namespace).Delete(svc.ObjectMeta.Name, nil)
			logErr("cleaning up svc", err)
			// ignore err
		}
	}
	return nil
}

func logErr(msg string, err error) {
	if err != nil {
		log.Printf("Error %v: %v", msg, err)
	}
}
