// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/url"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestFunctionServiceMap(t *testing.T) {
	logger := loggerfactory.GetLogger()

	m := makeFunctionServiceMap(logger, 0)
	fn := &metav1.ObjectMeta{Name: "foo", Namespace: metav1.NamespaceDefault}
	u, err := url.Parse("/foo012")
	if err != nil {
		t.Errorf("can't parse url")
	}

	m.assign(fn, u)

	v, err := m.lookup(fn)
	if err != nil {
		t.Errorf("Lookup error: %v", err)
	}
	if *v != *u {
		t.Errorf("Expected %#v, got %#v", u, v)
	}

	fn.Name = "bar"
	_, err2 := m.lookup(fn)
	if err2 == nil {
		t.Errorf("No error on missing entry")
	}
}
