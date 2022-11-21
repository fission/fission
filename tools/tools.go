//go:build tools
// +build tools

package tools

import (
	_ "github.com/elastic/crd-ref-docs"
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen"
)
