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

// GetResourceNamespace resolves the namespace a resource command operates in.
// Precedence: --namespace (also the returned "user-provided" namespace) wins;
// otherwise FISSION_DEFAULT_NAMESPACE; otherwise the kube-context namespace.
// The first return value is non-empty only when the user explicitly set
// --namespace, which cross-namespace checks rely on.
func (c *CommandActioner) GetResourceNamespace(input cli.Input) (namespace, currentNS string, err error) {
	if ns := input.String(flagkey.Namespace); ns != "" {
		console.Verbose(2, "Namespace for resource %s ", ns)
		return ns, ns, nil
	}

	if env := os.Getenv("FISSION_DEFAULT_NAMESPACE"); env != "" {
		console.Verbose(2, "Namespace for resource %s ", env)
		return "", env, nil
	}

	return "", c.Client().Namespace, nil
}

// ResolveNamespace returns the namespace a list command should operate in. It
// applies the same precedence as GetResourceNamespace, then collapses to
// metav1.NamespaceAll when --all-namespaces is set. This replaces the
// GetResourceNamespace + AllNamespaces block that was duplicated across every
// list command.
func (c *CommandActioner) ResolveNamespace(input cli.Input) (string, error) {
	_, namespace, err := c.GetResourceNamespace(input)
	if err != nil {
		return "", err
	}
	if input.Bool(flagkey.AllNamespaces) {
		namespace = metav1.NamespaceAll
	}
	return namespace, nil
}
