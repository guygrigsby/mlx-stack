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
	fmt.Fprintf(tw, "NAME\tGROUP\tMODE\tENGINE\tMODEL\tURL\t%s\tPID\tCURRENT\tTIER\tMEM(active/cache/peak)\tLAST TIMING\n", runHdr)
	names := make([]string, 0, len(s.Backends))
	idx := map[string]backendStatusJSON{}
	for _, b := range s.Backends {
		names = append(names, b.Name)
		idx[b.Name] = b
	}
	sort.Strings(names)
	for _, n := range names {
		b := idx[n]
		mem, tim := "-", "-"
		// Workers may be keyed by group-formatted name (e.g. chat[valkyrie]).
		for wname, wob := range s.Workers {
			if wname == b.Name || (b.CurrentName != "" && wname == fmt.Sprintf("%s[%s]", b.Group, b.CurrentName)) {
				if wob.LatestMem != nil {
					mem = fmt.Sprintf("%s / %s / %s",
						humanBytes(wob.LatestMem.Active),
						humanBytes(wob.LatestMem.Cache),
						humanBytes(wob.LatestMem.Peak))
				}
				if wob.LatestTiming != nil {
					tim = fmt.Sprintf("req=%s prefill=%.0fms@%.1ftps decode=%.0fms@%.1ftps",
						wob.LatestTiming.RequestID,
						wob.LatestTiming.PrefillMs, wob.LatestTiming.PrefillTPS,
						wob.LatestTiming.DecodeMs, wob.LatestTiming.DecodeTPS)
				}
				break
			}
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
		current := b.CurrentName
		if current == "" {
			current = "-"
		}
		// Group only means anything for swap backends (members sharing one
		// slot). For persistent/external it's a no-op, so don't imply otherwise.
		group := "-"
		if b.Mode == "swap" {
			group = b.Group
		}
		tier := b.Tier
		if tier == "" {
			tier = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			b.Name, group, b.Mode, b.Engine, shortModel(b.Model), b.URL, running, b.PID, current, tier, mem, tim)
	}
	tw.Flush()

	if s.CacheBudgetBytes > 0 {
		fmt.Fprintf(w, "\nCACHE  %s / %s\n", humanBytes(s.CacheUsedBytes), humanBytes(s.CacheBudgetBytes))
	}
}

// shortModel trims a local model directory to its last path segment (the model
// name) while leaving Hugging Face repo ids like "org/Model-6bit" intact.
func shortModel(m string) string {
	if m == "" {
		return "-"
	}
	if strings.HasPrefix(m, "/") {
		return filepath.Base(m)
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
