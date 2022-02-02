/*
Copyright 2022 The Fission Authors.
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

package healthcheck

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/fatih/color"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type CategoryID string

const (
	Kubernetes      CategoryID = "kubernetes"
	FissionServices CategoryID = "fission-services"
	FissionVersion  CategoryID = "fission-version"
)

var (
	okStatus   = color.New(color.FgGreen, color.Bold).SprintFunc()("\u221A") // √
	failStatus = color.New(color.FgRed, color.Bold).SprintFunc()("\u00D7")   // ×
)

// Category is a group of checkers
type Category struct {
	ID       CategoryID
	checkers []Checker
	enabled  bool
}

type Checker struct {
	successMsg string
	check      func() error
}

type Options struct {
	KubeContext   string
	FissionClient client.Interface
}

type HealthChecker struct {
	categories []*Category
	*Options

	kubeAPI          *kubernetes.Clientset
	fissionNamespace string
}

func isCompatibleVersion(minimalRequirementVersion [3]int, actualVersion [3]int) bool {
	if minimalRequirementVersion[0] < actualVersion[0] {
		return true
	}

	if (minimalRequirementVersion[0] == actualVersion[0]) && minimalRequirementVersion[1] < actualVersion[1] {
		return true
	}

	if (minimalRequirementVersion[0] == actualVersion[0]) && (minimalRequirementVersion[1] == actualVersion[1]) && (minimalRequirementVersion[2] <= actualVersion[2]) {
		return true
	}

	return false
}

func (hc *HealthChecker) CheckKubeVersion() (err error) {

	version, err := hc.kubeAPI.ServerVersion()
	if err != nil {
		return err
	}

	major, _ := strconv.Atoi(version.Major)
	minor, _ := strconv.Atoi(version.Minor)
	apiVersion := [3]int{major, minor, 0}
	minAPIVersion := [3]int{1, 19, 0}

	if !isCompatibleVersion(minAPIVersion, apiVersion) {
		return fmt.Errorf("kubernetes is on version %d.%d.%d, but version %d.%d.%d or more recent is required",
			apiVersion[0], apiVersion[1], apiVersion[2],
			minAPIVersion[0], minAPIVersion[1], minAPIVersion[2])
	}

	return nil
}

func (hc *HealthChecker) CheckServiceStatus(namespace string, name string) (err error) {
	depl, err := hc.kubeAPI.AppsV1().Deployments(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get %s deployment status", name)
	}

	if depl.Status.UnavailableReplicas > 0 || depl.Status.Replicas == 0 {
		return fmt.Errorf("%s deployment is not running", name)
	}

	_, err = hc.kubeAPI.CoreV1().Services(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get %s service status", name)
	}

	return nil
}

func (hc *HealthChecker) CheckFissionVersion() error {
	ver := util.GetVersion(hc.FissionClient)

	major, _ := strconv.Atoi(strings.Split(ver.Client["fission/core"].Version, ".")[0])
	minor, _ := strconv.Atoi(strings.Split(ver.Client["fission/core"].Version, ".")[1])
	fix, _ := strconv.Atoi(strings.Split(ver.Client["fission/core"].Version, ".")[2])

	apiVersion := [3]int{major, minor, fix}
	minAPIVersion := [3]int{1, 15, 1}
	if !isCompatibleVersion(minAPIVersion, apiVersion) {
		return fmt.Errorf("fission is on version %d.%d.%d, but version %d.%d.%d is the latest",
			apiVersion[0], apiVersion[1], apiVersion[2],
			minAPIVersion[0], minAPIVersion[1], minAPIVersion[2])
	}
	return nil
}

func NewCategory(id CategoryID, checkers []Checker, enabled bool) *Category {
	return &Category{
		ID:       id,
		checkers: checkers,
		enabled:  enabled,
	}
}

func (hc *HealthChecker) allCategories() []*Category {
	return []*Category{
		NewCategory(
			Kubernetes,
			[]Checker{
				{
					successMsg: "kubernetes version is compatible",
					check: func() (err error) {
						return hc.CheckKubeVersion()
					},
				},
			},
			false,
		),
		NewCategory(
			FissionServices,
			[]Checker{
				{
					successMsg: "controller is running fine",
					check: func() error {
						return hc.CheckServiceStatus(hc.fissionNamespace, "controller")
					},
				},
				{
					successMsg: "executor is running fine",
					check: func() error {
						return hc.CheckServiceStatus(hc.fissionNamespace, "executor")
					},
				},
				{
					successMsg: "router is running fine",
					check: func() error {
						return hc.CheckServiceStatus(hc.fissionNamespace, "router")
					},
				},
				{
					successMsg: "storagesvc is running fine",
					check: func() error {
						return hc.CheckServiceStatus(hc.fissionNamespace, "storagesvc")
					},
				},
			},
			false,
		),
		NewCategory(
			FissionVersion,
			[]Checker{
				{
					successMsg: "fission is up-to-date",
					check: func() error {
						return hc.CheckFissionVersion()
					},
				},
			},
			false,
		),
	}
}

func NewHealthChecker(categoryIDs []CategoryID, options *Options) *HealthChecker {
	hc := &HealthChecker{
		Options: options,
	}

	_, clientset, _ := util.GetKubernetesClient(hc.KubeContext)
	hc.kubeAPI = clientset
	hc.fissionNamespace = "fission"

	hc.categories = hc.allCategories()

	checkMap := map[CategoryID]struct{}{}
	for _, category := range categoryIDs {
		checkMap[category] = struct{}{}
	}
	for i := range hc.categories {
		if _, ok := checkMap[hc.categories[i].ID]; ok {
			hc.categories[i].enabled = true
		}
	}

	return hc
}

func RunChecks(hc *HealthChecker) {
	for _, c := range hc.categories {
		if c.enabled {
			fmt.Println(c.ID)
			fmt.Println(strings.Repeat("-", 20))
			for _, checker := range c.checkers {
				err := checker.check()
				if err != nil {
					fmt.Printf("%s %s\n", failStatus, err)
				} else {
					fmt.Printf("%s %s\n", okStatus, checker.successMsg)
				}
			}
			fmt.Printf("\n")
		}
	}
}
