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

// legendItem decodes one color. Built from the classes the diagram actually
// used, so the viewer never shows a swatch for a role that is not on screen —
// and never a color the viewer cannot decode.
type legendItem struct {
	Color string
	Label string
}

// pageData is the viewer's content. Subtitle carries the claim the diagram is
// evidence for ("Failed at charge · PaymentDeclined"), so the exhibit is never
// orphaned from what it shows.
type pageData struct {
	Title    string
	Subtitle string
	Diagram  string
	Legend   []legendItem
}

// legendFor decodes the classes a diagram used. In a run view a type class can
// only survive on a state that emits no events, so it is labelled as such
// rather than left to read as a status.
func legendFor(classes []string, runView bool) []legendItem {
	items := make([]legendItem, 0, len(classes))
	for _, c := range classes {
		s, ok := classStyles[c]
		if !ok {
			continue
		}
		label := s.Label
		// In a run view a type class only survives on a state that emits no
		// events, so label it as such rather than let it read as a status.
		if runView && isTypeClass(c) {
			label += " (not tracked)"
		}
		items = append(items, legendItem{Color: s.Fill, Label: label})
	}
	return items
}

// diagramPage renders the diagram client-side. Only the mermaid library is
// fetched; the workflow itself is inlined here and drawn in the user's own
// browser, so state names are never sent to a third-party renderer (which a
// mermaid.ink-style image URL would do).
//
// The layout keeps the diagram the loudest thing on the page: chrome is muted
// and borderless, the legend sits with the exhibit instead of across the page
// from it, and the diagram is centered in a bounded column.
const diagramPage = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>{{.Title}} — workflow graph</title>
<style>
  :root { --bg:#ffffff; --fg:#0f172a; --muted:#64748b; }
  body.night { --bg:#0f1115; --fg:#e6e6e6; --muted:#94a3b8; }
  body { margin:0; background:var(--bg); color:var(--fg);
         font-family: ui-sans-serif, system-ui, -apple-system, sans-serif;
         -webkit-font-smoothing:antialiased; }
  .wrap { max-width:1100px; margin:0 auto; padding:26px 24px 48px; }
  .head { display:flex; align-items:baseline; gap:12px; }
  h1 { margin:0; font-size:15px; font-weight:600; }
  .claim { color:var(--muted); font-size:13px; }
  #theme { margin-left:auto; cursor:pointer; border:0; background:transparent;
           color:var(--muted); font-size:12px; padding:2px 4px; }
  #theme:hover { color:var(--fg); }
  .legend { display:flex; flex-wrap:wrap; gap:16px; margin-top:10px;
            color:var(--muted); font-size:12px; }
  .legend span { display:inline-flex; align-items:center; gap:6px; }
  .legend i { width:9px; height:9px; border-radius:2px; }
  #d { margin-top:20px; display:flex; justify-content:center; overflow-x:auto; }
  #d svg { max-width:100%; height:auto; }
  #src { display:none; }
</style>
</head>
<body>
<div class="wrap">
  <div class="head">
    <h1>{{.Title}}</h1>
    {{with .Subtitle}}<span class="claim">{{.}}</span>{{end}}
    <button id="theme" title="Toggle day/night"></button>
  </div>
  {{with .Legend}}
  <div class="legend">
    {{range .}}<span><i style="background:{{.Color}}"></i>{{.Label}}</span>{{end}}
  </div>
  {{end}}
  <div id="d"></div>
</div>
<pre id="src">{{.Diagram}}</pre>
<script type="module">
  import mermaid from 'https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs';
  // The diagram is read back out of the DOM, never interpolated into JS: the
  // HTML template escapes it once, and it is only ever handled as text.
  const src = document.getElementById('src').textContent;
  const host = document.getElementById('d');
  const btn = document.getElementById('theme');
  const saved = localStorage.getItem('fission-wf-theme');
  let night = saved ? saved === 'night'
                    : window.matchMedia('(prefers-color-scheme: dark)').matches;

  async function render() {
    document.body.classList.toggle('night', night);
    btn.textContent = night ? '☀ day' : '☾ night';
    // Node fills are fixed mid-tones with white text, legible on either
    // canvas, so the toggle only moves mermaid's base theme: edges, labels
    // and region backgrounds.
    mermaid.initialize({ startOnLoad: false, theme: night ? 'dark' : 'default' });
    host.replaceChildren();
    const pre = document.createElement('pre');
    pre.className = 'mermaid';
    pre.textContent = src;
    host.appendChild(pre);
    await mermaid.run({ nodes: [pre] });
  }
  btn.addEventListener('click', () => {
    night = !night;
    localStorage.setItem('fission-wf-theme', night ? 'night' : 'day');
    render();
  });
  render();
</script>
</body>
</html>
`

// serveDiagram renders the page in the user's browser from an ephemeral local
// server, and blocks until the context is cancelled or the user interrupts.
func serveDiagram(ctx context.Context, data pageData) error {
	tmpl, err := template.New("page").Parse(diagramPage)
	if err != nil {
		return fmt.Errorf("building viewer page: %w", err)
	}
	var page bytes.Buffer
	if err := tmpl.Execute(&page, data); err != nil {
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
