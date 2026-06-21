// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type (
	CommandAction   func(input cli.Input) error
	CommandActioner struct{}
)

// ClusterOptionalAnnotation marks a cobra command that can run without a
// Kubernetes configuration (e.g. `function run-local --image`, which runs
// cluster-less). When it is set, the root PersistentPreRunE does not abort if a
// Fission client cannot be built; the command's own cluster-dependent paths
// surface a clear error via ClusterAvailable instead.
const ClusterOptionalAnnotation = "fission.io/cluster-optional"

var (
	once          = sync.Once{}
	defaultClient Client
)

func SetClientset(client Client) {
	once.Do(func() {
		defaultClient = client
	})
}

// ResetClientsetForTest clears the once-set default client so a unit test can
// install its own (fake) client deterministically. It exists because
// SetClientset is guarded by a sync.Once for the real CLI; tests must be able
// to reset that. Intended for tests only.
func ResetClientsetForTest() {
	once = sync.Once{}
	defaultClient = Client{}
}

func (c *CommandActioner) Client() Client {
	return defaultClient
}

// ClusterAvailable reports whether a Fission client was configured. It is false
// only for a cluster-optional command (see ClusterOptionalAnnotation) invoked
// without a usable kubeconfig; cluster-dependent paths should check it and
// return a clear error rather than dereferencing a nil client.
func (c *CommandActioner) ClusterAvailable() bool {
	return defaultClient.FissionClientSet != nil
}

func (c *CommandActioner) GetResourceNamespace(input cli.Input, deprecatedFlag string) (namespace, currentNS string, err error) {
	namespace = input.String(deprecatedFlag)
	currentNS = namespace

	if input.String(flagkey.Namespace) != "" {
		namespace = input.String(flagkey.Namespace)
		currentNS = namespace
		console.Verbose(2, "Namespace for resource %s ", currentNS)
		return namespace, currentNS, err
	}

	if namespace == "" {
		if os.Getenv("FISSION_DEFAULT_NAMESPACE") != "" {
			currentNS = os.Getenv("FISSION_DEFAULT_NAMESPACE")
		} else {
			currentNS = c.Client().Namespace
			return namespace, currentNS, err
		}
	}

	console.Verbose(2, "Namespace for resource %s ", currentNS)
	return namespace, currentNS, nil
}

// ResolveNamespace returns the namespace a list command should operate in. It
// applies the same precedence as GetResourceNamespace, then collapses to
// metav1.NamespaceAll when --all-namespaces is set. This replaces the
// GetResourceNamespace + AllNamespaces block that was duplicated across every
// list command.
func (c *CommandActioner) ResolveNamespace(input cli.Input, deprecatedFlag string) (string, error) {
	_, namespace, err := c.GetResourceNamespace(input, deprecatedFlag)
	if err != nil {
		return "", err
	}
	if input.Bool(flagkey.AllNamespaces) {
		namespace = metav1.NamespaceAll
	}
	return namespace, nil
}
