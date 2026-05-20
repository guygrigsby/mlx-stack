package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
)

type backendStatusJSON struct {
	Name        string `json:"name"`
	Group       string `json:"group"`
	Mode        string `json:"mode"`
	Engine      string `json:"engine"`
	URL         string `json:"url"`
	Running     bool   `json:"running"`
	PID         int    `json:"pid"`
	CurrentName string `json:"current_name"`
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

type statusJSON struct {
	Backends []backendStatusJSON      `json:"backends"`
	Workers  map[string]workerObsJSON `json:"workers"`
}

func renderStatus(w io.Writer, body []byte) {
	var s statusJSON
	if err := json.Unmarshal(body, &s); err != nil {
		fmt.Fprintln(w, "parse:", err)
		fmt.Fprintln(w, string(body))
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tGROUP\tMODE\tENGINE\tURL\tRUNNING\tPID\tCURRENT\tMEM(active/cache/peak)\tLAST TIMING")
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
		running := "no"
		if b.Running {
			running = "yes"
		}
		current := b.CurrentName
		if current == "" {
			current = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			b.Name, b.Group, b.Mode, b.Engine, b.URL, running, b.PID, current, mem, tim)
	}
	tw.Flush()
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
