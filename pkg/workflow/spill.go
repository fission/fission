// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
)

// spillThreshold is the inline step-I/O cap; larger documents live in the
// run's KV "io" keyspace and events carry the ref, keeping the log lean.
const spillThreshold = 64 * 1024

func ioScope(namespace, runName string) statestore.Scope {
	return statestore.Scope{
		Namespace: namespace,
		Owner:     "workflowrun/" + runName,
		Keyspace:  "io",
	}
}

func checkpointScope(namespace, runName string) statestore.Scope {
	return statestore.Scope{
		Namespace: namespace,
		Owner:     "workflowrun/" + runName,
		Keyspace:  "checkpoint",
	}
}

// spill stores doc under a per-(state, attempt) key. Keys are write-once in
// practice (W2: at most one result per attempt), so refs are immutable and
// dereferencing stays deterministic.
func spill(ctx context.Context, kv statestore.KVStore, namespace, runName, state string, attempt int32, doc json.RawMessage) (string, error) {
	key := fmt.Sprintf("%s/%d", state, attempt)
	if err := kv.Set(ctx, ioScope(namespace, runName), key, doc, statestore.SetOptions{}); err != nil {
		return "", fmt.Errorf("spilling %s: %w", key, err)
	}
	return key, nil
}

// derefFor builds the fold's spill resolver for one run.
func (e *Engine) derefFor(run *fv1.WorkflowRun) derefFn {
	return func(ref string) (json.RawMessage, error) {
		v, err := e.kv.Get(context.Background(), ioScope(run.Namespace, run.Name), ref)
		if err != nil {
			return nil, fmt.Errorf("dereferencing spilled document %q: %w", ref, err)
		}
		return v.Data, nil
	}
}

// loadCheckpoint restores the fold checkpoint; absence (or a decode failure,
// e.g. across an incompatible upgrade) just means folding from the start.
func (e *Engine) loadCheckpoint(ctx context.Context, run *fv1.WorkflowRun) (*RunState, error) {
	v, err := e.kv.Get(ctx, checkpointScope(run.Namespace, run.Name), "state")
	if err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			return newRunState(), nil
		}
		return nil, fmt.Errorf("loading checkpoint: %w", err)
	}
	s := newRunState()
	if jsonErr := json.Unmarshal(v.Data, s); jsonErr != nil {
		e.logger.V(1).Info("discarding undecodable checkpoint (re-folding from scratch)", "run", run.Name, "error", jsonErr)
		return newRunState(), nil
	}
	return s, nil
}

// saveCheckpoint is opportunistic: only every checkpointEvery events, and a
// failure is logged, never surfaced — checkpoints trade re-fold time, not
// correctness.
func (e *Engine) saveCheckpoint(ctx context.Context, run *fv1.WorkflowRun, s *RunState) {
	if s.LastSeq == 0 || s.LastSeq%checkpointEvery != 0 {
		return
	}
	data, err := json.Marshal(s)
	if err != nil {
		e.logger.Error(err, "encoding checkpoint", "run", run.Name)
		return
	}
	if err := e.kv.Set(ctx, checkpointScope(run.Namespace, run.Name), "state", data, statestore.SetOptions{}); err != nil {
		e.logger.V(1).Info("checkpoint write failed (harmless)", "run", run.Name, "error", err)
	}
}
