// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

func TestFunctionWait(t *testing.T) {
	ready := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "rdy", Namespace: "default"},
		Status: fv1.FunctionStatus{Conditions: []metav1.Condition{
			{Type: fv1.FunctionConditionReady, Status: metav1.ConditionTrue},
		}},
	}
	notReady := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "notrdy", Namespace: "default"},
		Status: fv1.FunctionStatus{Conditions: []metav1.Condition{
			{Type: fv1.FunctionConditionReady, Status: metav1.ConditionFalse},
		}},
	}
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(ready, notReady),
		Namespace:        "default",
	})

	t.Run("returns when condition already met", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.FnName, "rdy")
		in.Set(flagkey.WaitFor, "condition=Ready")
		in.Set(flagkey.WaitTimeout, 2*time.Second)
		if err := Wait(in); err != nil {
			t.Fatalf("expected success, got %v", err)
		}
	})

	t.Run("times out when condition not met", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.FnName, "notrdy")
		in.Set(flagkey.WaitFor, "condition=Ready")
		in.Set(flagkey.WaitTimeout, 30*time.Millisecond)
		if err := Wait(in); err == nil {
			t.Fatal("expected a timeout error")
		}
	})
}
