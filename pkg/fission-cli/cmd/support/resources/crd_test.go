// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

func TestMqtCleanMasksMetadataCredentials(t *testing.T) {
	t.Parallel()

	// This is the same map the keda scaler copies into the ScaledObject, so
	// masking it in only one of the two dumpers would leave the value in the
	// bundle regardless.
	mqt := fv1.MessageQueueTrigger{
		Name: "my-mqt", Namespace: "default",
		Spec: fv1.MessageQueueTriggerSpec{
			Metadata: map[string]string{
				"host":      "amqp://user:hunter2@rabbit:5672/",
				"queueName": "orders",
			},
		},
	}

	cleaned := mqtClean(mqt)

	assert.Equal(t, "-", cleaned.Spec.Metadata["host"])
	assert.Equal(t, "orders", cleaned.Spec.Metadata["queueName"],
		"non-credential metadata stays diagnostic")
	assert.Equal(t, "amqp://user:hunter2@rabbit:5672/", mqt.Spec.Metadata["host"],
		"the map is shared with the List result and must not be edited in place")
}

func TestMqtCleanLeavesTriggersWithoutCredentialsAlone(t *testing.T) {
	t.Parallel()

	mqt := fv1.MessageQueueTrigger{
		Spec: fv1.MessageQueueTriggerSpec{
			Metadata: map[string]string{"topic": "orders", "consumerGroup": "g1"},
		},
	}

	assert.Equal(t, mqt.Spec.Metadata, mqtClean(mqt).Spec.Metadata)
}

func TestCrdDumperMasksMessageQueueTriggerMetadata(t *testing.T) {
	t.Parallel()

	// Guards the wiring, not the helper: without mqtClean at the call site the
	// masking function can be perfectly correct and still never run.
	mqt := &fv1.MessageQueueTrigger{
		Name: "my-mqt", Namespace: "default", ResourceVersion: "1",
		Spec: fv1.MessageQueueTriggerSpec{
			Metadata: map[string]string{
				"host":      "amqp://user:hunter2@rabbit:5672/",
				"queueName": "orders",
			},
		},
	}

	dir := t.TempDir()
	d := CrdDumper{
		client:  cmd.Client{FissionClientSet: fissionfake.NewSimpleClientset(mqt)},
		crdType: CrdMessageQueueTrigger,
	}
	d.Dump(t.Context(), dir)

	files := dumpedFiles(t, dir)
	require.Len(t, files, 1)
	content, err := os.ReadFile(filepath.Join(dir, files[0]))
	require.NoError(t, err)

	assert.NotContains(t, string(content), "hunter2",
		"the mqt CRD dumper exposes the same metadata map as the ScaledObject dumper")
	assert.Contains(t, string(content), "orders")
}
