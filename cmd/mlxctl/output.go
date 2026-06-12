package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Renderers for the global -o/--output flag. Pattern: text mode keeps the
// human output, json mode emits a stable machine shape (the raw daemon body
// where one exists, a small object otherwise). Streaming commands (chat, run)
// are exempt.

// renderHealth: text prints "ok" (reachability is the answer); json prints the
// daemon's raw health body.
func renderHealth(w io.Writer, body []byte, asJSON bool) {
	if asJSON {
		fmt.Fprintln(w, strings.TrimSpace(string(body)))
		return
	}
	fmt.Fprintln(w, "ok")
}

// renderModelIDs: text prints one model ID per line; json prints the raw
// /v1/models body.
func renderModelIDs(w io.Writer, body []byte, asJSON bool) {
	if asJSON {
		fmt.Fprintln(w, strings.TrimSpace(string(body)))
		return
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal(body, &list)
	for _, m := range list.Data {
		fmt.Fprintln(w, m.ID)
	}
}

func renderReload(w io.Writer, res reloadResult, asJSON bool) {
	if asJSON {
		if res.Added == nil {
			res.Added = []string{}
		}
		if res.Skipped == nil {
			res.Skipped = []string{}
		}
		b, _ := json.Marshal(res)
		fmt.Fprintln(w, string(b))
		return
	}
	if len(res.Added) == 0 {
		fmt.Fprintln(w, "reloaded mlxd (no new backends)")
		return
	}
	fmt.Fprintf(w, "reloaded mlxd (added: %s)\n", strings.Join(res.Added, ", "))
}

// renderStopResult emits the summary for stop in json mode. Text mode prints
// per-backend lines as they happen (see newStopCmd), so it has nothing to add
// here. Empty slices encode as [] rather than null.
func renderStopResult(w io.Writer, stopped, failed []string, asJSON bool) {
	if !asJSON {
		return
	}
	if stopped == nil {
		stopped = []string{}
	}
	if failed == nil {
		failed = []string{}
	}
	b, _ := json.Marshal(struct {
		Stopped []string `json:"stopped"`
		Failed  []string `json:"failed"`
	}{stopped, failed})
	fmt.Fprintln(w, string(b))
}

// scanCandidate is the json shape for one scan result row.
type scanCandidate struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Engine   string `json:"engine"`
	InConfig bool   `json:"in_config"`
}

func renderScanJSON(w io.Writer, cands []scanCandidate) {
	if cands == nil {
		cands = []scanCandidate{}
	}
	b, _ := json.Marshal(cands)
	fmt.Fprintln(w, string(b))
}
