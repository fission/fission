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

package main

import "fmt"

func getDeploymentYaml() {
	fmt.Printf(`
apiVersion: v1
kind: Namespace
metadata:
  name: fission
  labels:
    name: fission

---
apiVersion: v1
kind: Namespace
metadata:
  name: fission-function
  labels:
    name: fission-function

---
apiVersion: v1
kind: Service
metadata:
  name: controller
  namespace: fission
  labels:
    svc: controller
spec:
  type: NodePort
  ports:
  - port: 80
    targetPort: 8888
    nodePort: 31313
  selector:
    svc: controller

---
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: controller
  namespace: fission
spec:
  replicas: 1
  template:
    metadata: 
      labels:
        svc: controller
    spec:
      containers:
      - name: controller
        image: fission/fission-bundle
        command: ["/fission-bundle"]
        args: ["--controllerPort", "8888", "--filepath", "/filestore"]

---
apiVersion: v1
kind: Service
metadata:
  name: router
  namespace: fission
  labels:
    svc: router
spec:
  type: NodePort
  ports:
  - port: 80
    targetPort: 8888
    nodePort: 31314
  selector:
    svc: router

---
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: router
  namespace: fission
spec:
  replicas: 1
  template:
    metadata: 
      labels:
        svc: router
    spec:
      containers:
      - name: router
        image: fission/fission-bundle
        command: ["/fission-bundle"]
        args: ["--routerPort", "8888"]

---
apiVersion: v1
kind: Service
metadata:
  name: poolmgr
  namespace: fission
  labels:
    svc: poolmgr
spec:
  ports:
  - port: 80
    targetPort: 8888
  selector:
    svc: poolmgr

---
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: poolmgr
  namespace: fission
spec:
  replicas: 1
  template:
    metadata: 
      labels:
        svc: poolmgr
    spec:
      containers:
      - name: poolmgr
        image: fission/fission-bundle
        command: ["/fission-bundle"]
        args: ["--poolmgrPort", "8888"]

---
apiVersion: v1
kind: Service
metadata:
  name: etcd
  namespace: fission
  labels:
    svc: etcd
spec:
  ports:
  - port: 2379
    targetPort: 2379
  selector:
    svc: etcd

---
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: etcd
  namespace: fission
spec:
  replicas: 1
  template:
    metadata:
      labels:
        svc: etcd
    spec:
      containers:
      - name: etcd
        image: quay.io/coreos/etcd
        env:
        - name: ETCD_LISTEN_CLIENT_URLS
          value: http://0.0.0.0:2379
        - name: ETCD_ADVERTISE_CLIENT_URLS
          value: http://etcd:2379
`)
}
