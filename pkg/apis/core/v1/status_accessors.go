// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// GetConditions returns a pointer to the object's Status.Conditions slice.
//
// These accessors let generic controller helpers (see pkg/controller) read and
// mutate a CRD's status conditions without knowing the concrete type. They are
// hand-written rather than generated because the code-generator does not emit
// status-condition accessors; keep one method per CRD that carries
// Status.Conditions.

func (p *Package) GetConditions() *[]metav1.Condition { return &p.Status.Conditions }

func (f *Function) GetConditions() *[]metav1.Condition { return &f.Status.Conditions }

func (e *Environment) GetConditions() *[]metav1.Condition { return &e.Status.Conditions }

func (h *HTTPTrigger) GetConditions() *[]metav1.Condition { return &h.Status.Conditions }

func (k *KubernetesWatchTrigger) GetConditions() *[]metav1.Condition {
	return &k.Status.Conditions
}

func (t *TimeTrigger) GetConditions() *[]metav1.Condition { return &t.Status.Conditions }

func (m *MessageQueueTrigger) GetConditions() *[]metav1.Condition {
	return &m.Status.Conditions
}

func (c *CanaryConfig) GetConditions() *[]metav1.Condition { return &c.Status.Conditions }

func (ft *FissionTenant) GetConditions() *[]metav1.Condition { return &ft.Status.Conditions }

func (w *Workflow) GetConditions() *[]metav1.Condition { return &w.Status.Conditions }

func (wr *WorkflowRun) GetConditions() *[]metav1.Condition { return &wr.Status.Conditions }

func (fa *FunctionAlias) GetConditions() *[]metav1.Condition { return &fa.Status.Conditions }
