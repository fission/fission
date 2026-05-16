/*
Copyright 2026 The Fission Authors.

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

package conditions

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func cond(t, status, reason string) metav1.Condition {
	return metav1.Condition{
		Type:    t,
		Status:  metav1.ConditionStatus(status),
		Reason:  reason,
		Message: reason + " message",
	}
}

func TestSet_InsertsNew(t *testing.T) {
	var conds []metav1.Condition
	before := metav1.Now()
	changed := Set(&conds, cond("Ready", "True", "AllReady"))
	require.True(t, changed, "insertion must report change")
	require.Len(t, conds, 1)
	require.Equal(t, "Ready", conds[0].Type)
	require.True(t,
		!conds[0].LastTransitionTime.Time.Before(before.Time),
		"LastTransitionTime must be set to roughly Now on insert (got %v, before=%v)",
		conds[0].LastTransitionTime, before,
	)
}

func TestSet_PreservesTimestampOnNoStatusChange(t *testing.T) {
	var conds []metav1.Condition
	Set(&conds, cond("Ready", "True", "AllReady"))
	originalTS := conds[0].LastTransitionTime

	// Force the wall clock to move so a stale impl would visibly drift.
	time.Sleep(25 * time.Millisecond)

	changed := Set(&conds, cond("Ready", "True", "StillReadyJustWithNewerReason"))
	require.True(t, changed, "Reason/Message updates must report change even when Status is unchanged")
	require.Equal(t, originalTS, conds[0].LastTransitionTime,
		"LastTransitionTime must be preserved while Status stays the same")
	require.Equal(t, "StillReadyJustWithNewerReason", conds[0].Reason)
}

func TestSet_UpdatesTimestampOnStatusFlip(t *testing.T) {
	var conds []metav1.Condition
	Set(&conds, cond("Ready", "True", "AllReady"))
	originalTS := conds[0].LastTransitionTime

	time.Sleep(25 * time.Millisecond)

	Set(&conds, cond("Ready", "False", "PackageBuildFailed"))
	require.True(t, conds[0].LastTransitionTime.After(originalTS.Time),
		"LastTransitionTime must advance when Status flips True→False")
	require.EqualValues(t, "False", conds[0].Status)
}

func TestSet_UpsertsByType(t *testing.T) {
	var conds []metav1.Condition
	Set(&conds, cond("Ready", "True", "r1"))
	Set(&conds, cond("Progressing", "False", "r2"))
	require.Len(t, conds, 2)

	// Setting an existing type updates in place — must not append duplicates.
	Set(&conds, cond("Ready", "False", "r3"))
	require.Len(t, conds, 2)
	require.Equal(t, "r3", Find(conds, "Ready").Reason)
}

func TestFind_NotPresent(t *testing.T) {
	conds := []metav1.Condition{cond("Ready", "True", "ok")}
	require.Nil(t, Find(conds, "DoesNotExist"))
}

func TestIsTrue(t *testing.T) {
	conds := []metav1.Condition{
		cond("Ready", "True", "ok"),
		cond("Progressing", "False", "stable"),
		cond("Degraded", "Unknown", "checking"),
	}
	require.True(t, IsTrue(conds, "Ready"))
	require.False(t, IsTrue(conds, "Progressing"))
	require.False(t, IsTrue(conds, "Degraded"))
	require.False(t, IsTrue(conds, "Missing"))
}
