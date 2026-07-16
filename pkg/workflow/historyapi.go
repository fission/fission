// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/go-logr/logr"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/statestore"
)

// HistoryEvent is one decoded log entry as served to the CLI; Seq/At come
// from the store envelope, the rest is the event.
type HistoryEvent struct {
	Seq int64  `json:"seq"`
	At  string `json:"at"`
	Event
}

// registerHistoryAPI serves the run history read path:
//
//	GET /history/{namespace}/{name}?uid=<run-uid>[&io=true]
//
// CRDs deliberately do not hold full history (etcd write amplification);
// this is the one place it is readable. Verified with the ServiceWorkflow
// HMAC channel — same posture as the other internal listeners (empty secret
// = pass-through).
func registerHistoryAPI(mux *http.ServeMux, logger logr.Logger, el statestore.EventLog, kv statestore.KVStore, master []byte) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		namespace := r.PathValue("namespace")
		name := r.PathValue("name")
		uid := r.URL.Query().Get("uid")
		if namespace == "" || name == "" || uid == "" {
			http.Error(w, "namespace, name, and uid are required", http.StatusBadRequest)
			return
		}

		stream := "wfrun/" + uid
		withIO := r.URL.Query().Get("io") == "true"

		var out []HistoryEvent
		var from int64
		for {
			events, err := el.Read(r.Context(), stream, from, readBatch)
			if err != nil {
				http.Error(w, "reading run history: "+err.Error(), http.StatusBadGateway)
				return
			}
			if len(events) == 0 {
				break
			}
			for _, se := range events {
				e, err := decodeEvent(se)
				if err != nil {
					http.Error(w, "corrupt history: "+err.Error(), http.StatusInternalServerError)
					return
				}
				if withIO && e.OutputRef != "" {
					if v, err := kv.Get(r.Context(), ioScope(namespace, name), e.OutputRef); err == nil {
						e.Output, e.OutputRef = v.Data, ""
					}
				}
				out = append(out, HistoryEvent{Seq: se.Seq, At: se.At.UTC().Format("2006-01-02T15:04:05.000Z07:00"), Event: e})
				from = se.Seq
			}
		}
		if out == nil {
			http.Error(w, "no history for this run (not started, or trimmed)", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	verifier := hmacauth.ServiceVerifier(master, []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET_OLD")),
		hmacauth.ServiceWorkflow, hmacauth.VerifierOpts{Logger: logger.WithName("history-auth")})
	mux.Handle("GET /history/{namespace}/{name}", verifier(handler))
}
