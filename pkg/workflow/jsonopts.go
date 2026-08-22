// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	"encoding/json/v2"
)

// The workflow document plane carries user-function-produced JSON opaquely in
// json.RawMessage fields (inputs, outputs, causes, checkpoints). Two option
// sets keep that passthrough contract intact under encoding/json/v2:
//
// docEncOpts: exact v1 emission. json/v2 validates RawMessage content on
// Marshal and — even with AllowInvalidUTF8 — substitutes U+FFFD inside a
// RawMessage instead of passing bytes through verbatim (the asymmetry pinned
// in jsonwire_compat_test.go). DefaultOptionsV1 is the only option set that
// re-emits stored documents byte-for-byte, which spill/re-spill and the event
// log require.
var docEncOpts = jsonv1.DefaultOptionsV1()

// docDecOpts: v1's decode leniency for durable and user-authored bytes.
// Checkpoints and events written by earlier releases — and function outputs
// admitted by the lenient ingress gate — may contain duplicate keys or
// invalid UTF-8 that v2 defaults reject.
var docDecOpts = json.JoinOptions(
	jsontext.AllowDuplicateNames(true),
	jsontext.AllowInvalidUTF8(true),
)
