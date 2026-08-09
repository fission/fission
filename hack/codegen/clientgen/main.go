// SPDX-FileCopyrightText: The Kubernetes Authors
// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Command clientgen is k8s.io/code-generator/cmd/client-gen with one change:
// the "private" name system is wrapped so a type whose lowercased name is a Go
// keyword does not produce source that fails to parse.
//
// Fission needs this because its Package CRD lowercases to "package". Upstream
// gengo has no keyword handling, so without this the generated typed client is
// rejected by the formatter with "expected ')', found 'package'".
//
// hack/update-codegen.sh copies the upstream code-generator module to a temp
// directory and drops this file over cmd/client-gen/main.go. kube_codegen.sh
// runs `go install` from its own directory, so the copy is what gets built.
// Delete this package once upstream handles Go keywords itself.
package main

import (
	"flag"
	"go/token"
	"slices"

	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	"k8s.io/code-generator/cmd/client-gen/args"
	"k8s.io/code-generator/cmd/client-gen/generators"
	"k8s.io/code-generator/pkg/util"
	"k8s.io/gengo/v2"
	"k8s.io/gengo/v2/generator"
	"k8s.io/gengo/v2/namer"
	"k8s.io/gengo/v2/types"
)

// keywordSafeNamer suffixes an underscore when the wrapped namer produces a Go
// keyword.
//
// The underscore must trail, never lead. client_generator.go derives the output
// file name from this same namer:
//
//	OutputFilename: strings.ToLower(c.Namers["private"].Name(t)) + ".go"
//
// so a leading underscore would emit _package.go, and the Go toolchain ignores
// files whose names begin with "_".
type keywordSafeNamer struct{ namer.Namer }

func (k keywordSafeNamer) Name(t *types.Type) string {
	n := k.Namer.Name(t)
	if token.IsKeyword(n) {
		return n + "_"
	}
	return n
}

func main() {
	klog.InitFlags(nil)
	args := args.New()

	args.AddFlags(pflag.CommandLine, "k8s.io/kubernetes/pkg/apis") // TODO: move this input path out of client-gen
	flag.Set("logtostderr", "true")
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	// add group version package as input dirs for gengo
	inputPkgs := []string{}
	for _, pkg := range args.Groups {
		for _, v := range pkg.Versions {
			inputPkgs = append(inputPkgs, v.Package)
		}
	}
	// ensure stable code generation output
	slices.Sort(inputPkgs)

	if err := args.Validate(); err != nil {
		klog.Fatalf("Error: %v", err)
	}

	myTargets := func(context *generator.Context) []generator.Target {
		return generators.GetTargets(context, args)
	}

	nameSystems := generators.NameSystems(util.PluralExceptionListToMapOrDie(args.PluralExceptions))
	nameSystems["private"] = keywordSafeNamer{nameSystems["private"]}

	if err := gengo.Execute(
		nameSystems,
		generators.DefaultNameSystem(),
		myTargets,
		gengo.StdBuildTag,
		inputPkgs,
	); err != nil {
		klog.Fatalf("Error: %v", err)
	}
}
