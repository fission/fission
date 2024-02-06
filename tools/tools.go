//go:build tools
// +build tools

package tools

import (
	_ "github.com/elastic/crd-ref-docs"
	_ "k8s.io/code-generator"
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen"
)
