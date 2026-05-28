// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/labels"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// TestGetInformerLabelByExecutor guards against the regression where the
// returned selector was empty (labels.Selector.Add's result was discarded),
// so executor informers matched every pod cluster-wide and OOMed at scale
// (issue #2775). The selector must be non-empty and match only pods carrying
// the executor-type label.
func TestGetInformerLabelByExecutor(t *testing.T) {
	selector, err := GetInformerLabelByExecutor(fv1.ExecutorTypePoolmgr)
	require.NoError(t, err)

	assert.False(t, selector.Empty(), "selector must filter by executor type, not match everything")
	assert.Equal(t, fv1.EXECUTOR_TYPE+"=="+string(fv1.ExecutorTypePoolmgr), selector.String())

	assert.True(t, selector.Matches(labels.Set{fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypePoolmgr)}),
		"must match a pod with the matching executor-type label")
	assert.False(t, selector.Matches(labels.Set{fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypeNewdeploy)}),
		"must not match a pod with a different executor type")
	assert.False(t, selector.Matches(labels.Set{}),
		"must not match an unlabelled pod")
}
