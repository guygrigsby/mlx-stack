package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

const (
	ansiReset  = "\033[0m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	// ansiDefault is the default-foreground SGR. It has no visible effect but
	// the same byte length as a color code, so wrapping the RUNNING header in
	// it gives that header cell the same escape overhead as the colored data
	// cells. tabwriter counts escape bytes toward column width, so uniform
	// overhead across the column keeps every other column aligned.
	ansiDefault = "\033[39m"
)

// useColor reports whether status output to w should be colorized: only when w
// is a terminal and NO_COLOR is unset. A bytes.Buffer or a pipe yields false,
// keeping piped/redirected output and tests free of escape codes.
func useColor(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

type backendStatusJSON struct {
	Name        string `json:"name"`
	Group       string `json:"group"`
	Mode        string `json:"mode"`
	Engine      string `json:"engine"`
	URL         string `json:"url"`
	Running     bool   `json:"running"`
	State       string `json:"state"`
	Model       string `json:"model"`
	PID         int    `json:"pid"`
	CurrentName string `json:"current_name"`
	Tier        string `json:"tier"`
}

type workerObsJSON struct {
	LatestMem *struct {
		Active int64 `json:"Active"`
		Cache  int64 `json:"Cache"`
		Peak   int64 `json:"Peak"`
	} `json:"latest_mem"`
	LatestTiming *struct {
		RequestID  string  `json:"RequestID"`
		PrefillMs  float64 `json:"PrefillMs"`
		PrefillTPS float64 `json:"PrefillTPS"`
		DecodeMs   float64 `json:"DecodeMs"`
		DecodeTPS  float64 `json:"DecodeTPS"`
	} `json:"latest_timing"`
	LastTimingAt int64 `json:"last_timing_at"`
	ActiveReq    *struct {
		RequestID string  `json:"RequestID"`
		Tokens    int     `json:"Tokens"`
		TPS       float64 `json:"TPS"`
		Max       int     `json:"Max"`
	} `json:"active_req"`
}

type routerJSON struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	ExtraPorts []int  `json:"extra_ports"`
}

type statusJSON struct {
	Router           routerJSON               `json:"router"`
	Backends         []backendStatusJSON      `json:"backends"`
	Workers          map[string]workerObsJSON `json:"workers"`
	Active           map[string]int           `json:"active"`
	CacheUsedBytes   int64                    `json:"cache_used_bytes"`
	CacheBudgetBytes int64                    `json:"cache_budget_bytes"`
}

func renderStatus(w io.Writer, body []byte) {
	var s statusJSON
	if err := json.Unmarshal(body, &s); err != nil {
		fmt.Fprintln(w, "parse:", err)
		fmt.Fprintln(w, string(body))
		return
	}
	color := useColor(w)

	// Router section: the daemon answered, so it's up. Shown above the
	// backends with its listen port(s).
	if s.Router.Port > 0 {
		up := "running"
		if color {
			up = ansiGreen + up + ansiReset
		}
		fmt.Fprintf(w, "ROUTER  http://%s:%d  %s\n", s.Router.Host, s.Router.Port, up)
		for _, p := range s.Router.ExtraPorts {
			fmt.Fprintf(w, "        http://%s:%d\n", s.Router.Host, p)
		}
		fmt.Fprintln(w)
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	runHdr := "RUNNING"
	if color {
		runHdr = ansiDefault + runHdr + ansiReset
	}
	fmt.Fprintf(tw, "NAME\tSLOT\tTYPE\tKIND\tMODEL\t%s\tTIER\tMEM(active/cache/peak)\tACTIVITY\n", runHdr)
	names := make([]string, 0, len(s.Backends))
	idx := map[string]backendStatusJSON{}
	for _, b := range s.Backends {
		names = append(names, b.Name)
		idx[b.Name] = b
	}
	sort.Strings(names)
	for _, n := range names {
		b := idx[n]
		mem, activity := "-", "-"
		inFlight := s.Active[b.Name]
		// Workers may be keyed by group-formatted name (e.g. chat[valkyrie]).
		for wname, wob := range s.Workers {
			if wname == b.Name || (b.CurrentName != "" && wname == fmt.Sprintf("%s[%s]", b.Group, b.CurrentName)) {
				if wob.LatestMem != nil {
					mem = fmt.Sprintf("%s / %s / %s",
						humanBytes(wob.LatestMem.Active),
						humanBytes(wob.LatestMem.Cache),
						humanBytes(wob.LatestMem.Peak))
				}
				// Live decode progress takes priority over historical timing.
				if wob.ActiveReq != nil {
					ar := wob.ActiveReq
					if ar.Max > 0 {
						pct := ar.Tokens * 100 / ar.Max
						activity = fmt.Sprintf("LIVE %.1ft/s  %d tok  %d%%", ar.TPS, ar.Tokens, pct)
					} else {
						activity = fmt.Sprintf("LIVE %.1ft/s  %d tok", ar.TPS, ar.Tokens)
					}
				} else if wob.LatestTiming != nil {
					ago := ""
					if wob.LastTimingAt > 0 {
						d := time.Since(time.Unix(wob.LastTimingAt, 0)).Round(time.Second)
						ago = fmt.Sprintf("  (%s ago)", d)
					}
					activity = fmt.Sprintf("%.1ft/s%s", wob.LatestTiming.DecodeTPS, ago)
				} else if inFlight > 0 {
					activity = fmt.Sprintf("%d req", inFlight)
				}
				break
			}
		}
		// For backends with no worker obs (audio/embed/external), fall back to in-flight count.
		if activity == "-" && inFlight > 0 {
			activity = fmt.Sprintf("%d req", inFlight)
		}
		// Run cell: ready=yes (green), loading=loading (yellow), unhealthy=
		// wedged (red, loaded but not answering), stopped=no (red). Falls back
		// to Running for an older daemon that doesn't report state.
		running, rc := "no", ansiRed
		switch b.State {
		case "ready":
			running, rc = "yes", ansiGreen
		case "loading":
			running, rc = "loading", ansiYellow
		case "unhealthy":
			running, rc = "wedged", ansiRed
		case "stopped", "":
			if b.State == "" && b.Running {
				running, rc = "yes", ansiGreen
			}
		}
		if color {
			running = rc + running + ansiReset
		}
		// Slot is the addressable name; for a multi-model slot it groups the
		// members, for warm/remote it equals the name.
		slot := b.Group
		if slot == "" {
			slot = b.Name
		}
		kind := kindWord(b.Engine)
		tier := b.Tier
		if tier == "" {
			tier = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			b.Name, slot, slotType(b.Mode), kind, shortModel(b.Model), running, tier, mem, activity)
	}
	tw.Flush()

	if s.CacheBudgetBytes > 0 {
		fmt.Fprintf(w, "\nCACHE  %s / %s\n", humanBytes(s.CacheUsedBytes), humanBytes(s.CacheBudgetBytes))
	}
}

// kindWord translates an internal engine id to the human word shown in list
// output. "" (external/unknown) falls back to a dash.
func kindWord(engine string) string {
	switch engine {
	case "lm":
		return "chat"
	case "vlm":
		return "vision"
	case "audio":
		return "speech"
	case "embed":
		return "embeddings"
	default:
		return "-"
	}
}

// slotType translates an internal mode to the user word for a slot's nature.
func slotType(mode string) string {
	switch mode {
	case "swap":
		return "slot"
	case "persistent":
		return "warm"
	case "external":
		return "remote"
	default:
		return mode
	}
}

// slotState is the one-word live state of a single-model slot, in user
// vocabulary: remote, warm, loaded, loading, wedged, or idle.
func slotState(sp config.BackendSpec, live backendStatusJSON) string {
	if sp.Remote {
		return "remote"
	}
	switch live.State {
	case "ready":
		if sp.Warm {
			return "warm"
		}
		return "loaded"
	case "loading":
		return "loading"
	case "unhealthy":
		return "wedged"
	default:
		if sp.Warm {
			return "warm·stopped"
		}
		return "idle"
	}
}

// stateColor wraps a state word in the matching color when enabled.
func stateColor(word string, color bool) string {
	if !color {
		return word
	}
	switch word {
	case "loaded", "warm":
		return ansiGreen + word + ansiReset
	case "loading":
		return ansiYellow + word + ansiReset
	case "wedged", "warm·stopped":
		return ansiRed + word + ansiReset
	default:
		return word
	}
}

// renderList prints the friendly slot view: every addressable slot, the models
// it can load (indented when more than one), and which is hot. Membership comes
// from the config; live state (which member is loaded, health) from the status
// snapshot. This is the "what can I talk to" view; `status` stays the detailed
// power table.
func renderList(w io.Writer, specs []config.BackendSpec, s statusJSON) {
	color := useColor(w)
	st := map[string]backendStatusJSON{}
	for _, b := range s.Backends {
		st[b.Name] = b
	}

	order := []string{}
	bySlot := map[string][]config.BackendSpec{}
	for _, sp := range specs {
		if _, ok := bySlot[sp.Slot]; !ok {
			order = append(order, sp.Slot)
		}
		bySlot[sp.Slot] = append(bySlot[sp.Slot], sp)
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SLOT / MODEL\tKIND\tSTATE")
	for _, slot := range order {
		members := bySlot[slot]
		live := st[slot]
		if len(members) == 1 {
			m := members[0]
			fmt.Fprintf(tw, "%s\t%s\t%s\n", slot, kindWord(m.Engine), stateColor(slotState(m, live), color))
			continue
		}
		// Multi-model slot: header line then one indented row per candidate.
		loaded := 0
		if live.CurrentName != "" {
			loaded = 1
		}
		fmt.Fprintf(tw, "%s\tslot\t%d of %d loaded\n", slot, loaded, len(members))
		for _, m := range members {
			state, glyph := "idle", "○"
			if m.Name == live.CurrentName {
				state, glyph = slotState(m, live), "●"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s %s\n", m.Name, kindWord(m.Engine), glyph, stateColor(state, color))
		}
	}
	tw.Flush()
}

// shortModel trims a local model directory to its last path segment (the model
// name) while leaving Hugging Face repo ids like "org/Model-6bit" intact.
func shortModel(m string) string {
	if m == "" {
		return "-"
	}
	if strings.HasPrefix(m, "/") {
		m = filepath.Base(m)
	}
	if len(m) > 28 {
		return m[:27] + "…"
	}
	return m
}

func humanBytes(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1fG", float64(b)/GB)
	case b >= MB:
		return fmt.Sprintf("%.1fM", float64(b)/MB)
	case b >= KB:
		return fmt.Sprintf("%.1fK", float64(b)/KB)
	default:
		return fmt.Sprintf("%dB", b)
	}
}
