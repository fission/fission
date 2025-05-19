package main

import (
	"fmt"
	"log"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"

	"github.com/fission/fission/cmd/fission-cli/app"
	"github.com/fission/fission/pkg/fission-cli/cmd"
)

const fmTemplate = `---
title: %s
slug: %s
url: %s
---
`

const baseURL = "/docs/reference/fission-cli/"

var filePrepender = func(filename string) string {
	name := filepath.Base(filename)
	base := strings.TrimSuffix(name, path.Ext(name))
	url := baseURL + strings.ToLower(base) + "/"
	return fmt.Sprintf(fmTemplate, strings.ReplaceAll(base, "_", " "), base, url)
}

var linkHandler = func(name string) string {
	base := strings.TrimSuffix(name, path.Ext(name))
	return baseURL + strings.ToLower(base) + "/"
}

func main() {
	var outdir string
	var rootCmd = &cobra.Command{
		Use:   "fission-cli-docs",
		Short: "Generate docs for fission-cli",
		Long:  "Generate docs for fission-cli",
		Run: func(command *cobra.Command, args []string) {
			log.Printf("Generating docs in directory %s", outdir)
			fissionApp := app.App(cmd.ClientOptions{})
			fissionApp.DisableAutoGenTag = true
			fissionApp.Short = "Serverless framework for Kubernetes"
			err := doc.GenMarkdownTreeCustom(fissionApp, outdir, filePrepender, linkHandler)
			if err != nil {
				log.Fatal(err)
			}
		},
	}
	rootCmd.Flags().StringVarP(&outdir, "outdir", "o", "/tmp/cli-docs", "Output directory")

	err := rootCmd.Execute()
	if err != nil {
		log.Fatal(err)
	}
}
