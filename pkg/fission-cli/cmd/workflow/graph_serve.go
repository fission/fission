// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"
)

// diagramPage renders the diagram client-side. Only the mermaid library is
// fetched from the CDN — the workflow itself is inlined here and rendered in
// the user's own browser, so state names are never sent to a third-party
// renderer (which a mermaid.ink-style image URL would do).
const diagramPage = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>{{.Title}} — workflow graph</title>
<style>
  body { margin:0; background:#1a1a1a; color:#e6e6e6;
         font-family: ui-sans-serif, system-ui, -apple-system, sans-serif; }
  header { padding:10px 16px; border-bottom:1px solid #333; font-size:13px; }
  header b { color:#fff; }
  .legend { float:right; color:#999; }
  .legend i { display:inline-block; width:10px; height:10px; border-radius:2px;
              margin:0 5px 0 12px; vertical-align:middle; }
  #d { padding:16px; overflow:auto; }
</style>
</head>
<body>
<header><b>{{.Title}}</b>{{with .Legend}}<span class="legend">{{.}}</span>{{end}}</header>
<div id="d"><pre class="mermaid">{{.Diagram}}</pre></div>
<script type="module">
  import mermaid from 'https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs';
  mermaid.initialize({ startOnLoad: true, theme: 'dark' });
</script>
</body>
</html>
`

// runLegend labels the run-overlay colors. It is a fixed string (never user
// input), so it is safe to inject as HTML. Choice/Succeed/Fail states keep
// their type color: they are resolved in the fold and emit no events, so the
// history cannot say whether the run passed through them.
const runLegend = `<i style="background:#4ad96a"></i>succeeded` +
	`<i style="background:#d9a04a"></i>active` +
	`<i style="background:#d94a4a"></i>failed` +
	`<i style="background:#555"></i>unreached` +
	`<i style="background:#a07ed9"></i>routing (not tracked)`

// serveDiagram renders diagram in the user's browser from an ephemeral local
// server, and blocks until the context is cancelled or the user interrupts.
func serveDiagram(ctx context.Context, diagram, title, legend string) error {
	tmpl, err := template.New("page").Parse(diagramPage)
	if err != nil {
		return fmt.Errorf("building viewer page: %w", err)
	}
	var page bytes.Buffer
	err = tmpl.Execute(&page, struct {
		Title   string
		Diagram string
		Legend  template.HTML
	}{Title: title, Diagram: diagram, Legend: template.HTML(legend)}) //nolint:gosec // fixed legend, never user input
	if err != nil {
		return fmt.Errorf("building viewer page: %w", err)
	}

	// Port 0: the OS picks a free one, so concurrent viewers never collide.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("starting local viewer: %w", err)
	}
	defer func() { _ = ln.Close() }()

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(page.Bytes())
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()

	url := "http://" + ln.Addr().String()
	fmt.Printf("workflow graph served at %s (Ctrl-C to stop)\n", url)
	openBrowser(url)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	select {
	case <-ctx.Done():
	case <-sigCh:
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return srv.Shutdown(shutCtx)
}

// openBrowser is best-effort: on a headless box the printed URL is the
// fallback, so a failure to launch is not an error.
func openBrowser(url string) {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		name = "xdg-open"
	}
	_ = exec.Command(name, append(args, url)...).Start()
}
