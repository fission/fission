// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestImageVolumeSupported(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		major string
		minor string
		want  bool
	}{
		{"floor 1.32", "1", "32", false},
		{"first supported 1.33", "1", "33", true},
		{"current 1.36", "1", "36", true},
		{"vendor suffix 1.33+", "1", "33+", true},
		{"vendor suffix 1.32+", "1", "32+", false},
		{"major 2", "2", "0", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := k8sfake.NewSimpleClientset()
			disco, ok := client.Discovery().(*fakediscovery.FakeDiscovery)
			require.True(t, ok)
			disco.FakedServerVersion = &version.Info{Major: tc.major, Minor: tc.minor}

			got, err := ImageVolumeSupported(client.Discovery())
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestOCIImageVolumeEnabled(t *testing.T) {
	t.Setenv("ENABLE_OCI_IMAGE_VOLUME", "")
	assert.False(t, OCIImageVolumeEnabled(), "default must be off")

	t.Setenv("ENABLE_OCI_IMAGE_VOLUME", "true")
	assert.True(t, OCIImageVolumeEnabled())

	t.Setenv("ENABLE_OCI_IMAGE_VOLUME", "false")
	assert.False(t, OCIImageVolumeEnabled())
}
