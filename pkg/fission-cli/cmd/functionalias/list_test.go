// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package functionalias

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestFilterByFunction(t *testing.T) {
	items := []fv1.FunctionAlias{
		{ObjectMeta: metav1.ObjectMeta{Name: "a"}, Spec: fv1.FunctionAliasSpec{FunctionName: "hello"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b"}, Spec: fv1.FunctionAliasSpec{FunctionName: "other"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "c"}, Spec: fv1.FunctionAliasSpec{FunctionName: "hello"}},
	}

	t.Run("empty filter returns everything", func(t *testing.T) {
		got := filterByFunction(items, "")
		assert.Len(t, got, 3)
	})

	t.Run("filters to the named function", func(t *testing.T) {
		got := filterByFunction(items, "hello")
		names := make([]string, 0, len(got))
		for _, a := range got {
			names = append(names, a.Name)
		}
		assert.Equal(t, []string{"a", "c"}, names)
	})

	t.Run("no matches returns empty, not nil-panicking", func(t *testing.T) {
		got := filterByFunction(items, "nope")
		assert.Empty(t, got)
	})
}

func TestAliasRow(t *testing.T) {
	weight := 60
	a := &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod"},
		Spec: fv1.FunctionAliasSpec{
			FunctionName:     "hello",
			Version:          "hello-v1",
			Weight:           &weight,
			SecondaryVersion: "hello-v2",
		},
		Status: fv1.FunctionAliasStatus{ResolvedVersion: "hello-v1"},
	}
	row := aliasRow(a)
	assert.Equal(t, []string{"prod", "hello", "hello-v1", "", "60", "hello-v2", "hello-v1"}, row)
}

func TestAliasRowNoWeightOrResolution(t *testing.T) {
	a := &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
	}
	row := aliasRow(a)
	assert.Equal(t, "<none>", row[4], "weight column should show <none> when unset")
	assert.Equal(t, "<none>", row[6], "resolved-version column should show <none> before the controller observes it")
}
