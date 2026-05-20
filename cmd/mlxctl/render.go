package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
)

type workerObsJSON struct {
	Name     string `json:"name"`
	LatestMem *struct {
		Active int64 `json:"active"`
		Cache  int64 `json:"cache"`
		Peak   int64 `json:"peak"`
	} `json:"latest_mem"`
	LatestTiming *struct {
		RequestID    string  `json:"RequestID"`
		PromptTokens int     `json:"PromptTokens"`
		PrefillMs    float64 `json:"PrefillMs"`
		PrefillTPS   float64 `json:"PrefillTPS"`
		DecodeMs     float64 `json:"DecodeMs"`
		DecodeTPS    float64 `json:"DecodeTPS"`
	} `json:"latest_timing"`
}

type statusJSON struct {
	Chat struct {
		CurrentProfile string   `json:"current_profile"`
		PID            int      `json:"pid"`
		URL            string   `json:"url"`
		Profiles       []string `json:"profiles"`
	} `json:"chat"`
	Tags *struct {
		Alias   string `json:"alias"`
		PID     int    `json:"pid"`
		URL     string `json:"url"`
		Running bool   `json:"running"`
	} `json:"tags"`
	Workers map[string]workerObsJSON `json:"workers"`
}

func renderStatus(w io.Writer, body []byte) {
	var s statusJSON
	if err := json.Unmarshal(body, &s); err != nil {
		fmt.Fprintln(w, "could not parse status JSON:", err)
		fmt.Fprintln(w, string(body))
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKER\tPROFILE/ALIAS\tPID\tURL\tMEM(active/cache/peak)\tLAST TIMING")

	// Chat row. Worker key can be "chat[\"profile\"]" or "chat[profile]".
	chatKey := `chat["` + s.Chat.CurrentProfile + `"]`
	chatKeyAlt := "chat[" + s.Chat.CurrentProfile + "]"
	memChat := workerMem(s.Workers, chatKey)
	if memChat == "" {
		memChat = workerMem(s.Workers, chatKeyAlt)
	}
	timChat := workerTiming(s.Workers, chatKey)
	if timChat == "" {
		timChat = workerTiming(s.Workers, chatKeyAlt)
	}
	fmt.Fprintf(tw, "chat\t%s\t%d\t%s\t%s\t%s\n",
		emptyDefault(s.Chat.CurrentProfile, "(stopped)"),
		s.Chat.PID,
		emptyDefault(s.Chat.URL, "-"),
		emptyDefault(memChat, "-"),
		emptyDefault(timChat, "-"),
	)

	// Tags row (if configured).
	if s.Tags != nil {
		memTags := workerMem(s.Workers, "tags")
		fmt.Fprintf(tw, "tags\t%s\t%d\t%s\t%s\t-\n",
			s.Tags.Alias, s.Tags.PID, emptyDefault(s.Tags.URL, "-"), emptyDefault(memTags, "-"),
		)
	}

	// Any other workers (embed, tts, kokoro, etc).
	skip := map[string]bool{
		chatKey:    true,
		chatKeyAlt: true,
		"chat":     true,
		"tags":     true,
	}
	others := []string{}
	for name := range s.Workers {
		if !skip[name] {
			others = append(others, name)
		}
	}
	sort.Strings(others)
	for _, name := range others {
		mem := workerMem(s.Workers, name)
		tim := workerTiming(s.Workers, name)
		fmt.Fprintf(tw, "%s\t-\t-\t-\t%s\t%s\n", name, emptyDefault(mem, "-"), emptyDefault(tim, "-"))
	}
	tw.Flush()
	fmt.Fprintf(w, "\nProfiles available: %s\n", listOrDash(s.Chat.Profiles))
}

func workerMem(workers map[string]workerObsJSON, name string) string {
	w, ok := workers[name]
	if !ok || w.LatestMem == nil {
		return ""
	}
	return fmt.Sprintf("%s / %s / %s",
		humanBytes(w.LatestMem.Active),
		humanBytes(w.LatestMem.Cache),
		humanBytes(w.LatestMem.Peak),
	)
}

func workerTiming(workers map[string]workerObsJSON, name string) string {
	w, ok := workers[name]
	if !ok || w.LatestTiming == nil {
		return ""
	}
	return fmt.Sprintf("req=%s prefill=%.0fms@%.1ftps decode=%.0fms@%.1ftps",
		w.LatestTiming.RequestID,
		w.LatestTiming.PrefillMs,
		w.LatestTiming.PrefillTPS,
		w.LatestTiming.DecodeMs,
		w.LatestTiming.DecodeTPS,
	)
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

func emptyDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func listOrDash(ss []string) string {
	if len(ss) == 0 {
		return "-"
	}
	return fmt.Sprint(ss)
}
