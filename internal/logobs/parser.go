package logobs

import (
	"regexp"
	"strconv"
	"strings"
)

type Kind int

const (
	KindUnknown Kind = iota
	KindMem
	KindTiming
	KindWatchdogArmed
	KindWatchdogTrigger
	KindStarting
)

type MemSnapshot struct {
	Active int64
	Cache  int64
	Peak   int64
}

type Timing struct {
	RequestID    string
	PromptTokens int
	PrefillMs    float64
	PrefillTPS   float64
	DecodeMs     float64
	DecodeTPS    float64
}

type WatchdogEvent struct {
	Baseline int64
	Trigger  int64
	Active   int64
}

type Event struct {
	Raw      string
	Worker   string
	Kind     Kind
	Mem      MemSnapshot
	Timing   Timing
	Watchdog WatchdogEvent
}

const prefix = "[mlx-launch] "

var (
	reMem    = regexp.MustCompile(`^mem: active=(\d+) cache=(\d+) peak=(\d+)`)
	reTiming = regexp.MustCompile(`^req=(\S+) prompt=(\d+)t prefill=([\d.]+)ms@([\d.]+)tps decode=([\d.]+)ms@([\d.]+)tps`)
	reWdArm  = regexp.MustCompile(`^WATCHDOG: armed\. baseline=(\d+) trigger=(\d+)`)
	reWdTrig = regexp.MustCompile(`^WATCHDOG: active=(\d+) > trigger=(\d+)`)
)

func Parse(line string) (Event, bool) {
	if !strings.HasPrefix(line, prefix) {
		return Event{}, false
	}
	body := strings.TrimPrefix(line, prefix)
	ev := Event{Raw: line, Kind: KindUnknown}

	if m := reMem.FindStringSubmatch(body); m != nil {
		ev.Kind = KindMem
		ev.Mem.Active = mustAtoi64(m[1])
		ev.Mem.Cache = mustAtoi64(m[2])
		ev.Mem.Peak = mustAtoi64(m[3])
		return ev, true
	}
	if m := reTiming.FindStringSubmatch(body); m != nil {
		ev.Kind = KindTiming
		ev.Timing.RequestID = m[1]
		ev.Timing.PromptTokens, _ = strconv.Atoi(m[2])
		ev.Timing.PrefillMs, _ = strconv.ParseFloat(m[3], 64)
		ev.Timing.PrefillTPS, _ = strconv.ParseFloat(m[4], 64)
		ev.Timing.DecodeMs, _ = strconv.ParseFloat(m[5], 64)
		ev.Timing.DecodeTPS, _ = strconv.ParseFloat(m[6], 64)
		return ev, true
	}
	if m := reWdArm.FindStringSubmatch(body); m != nil {
		ev.Kind = KindWatchdogArmed
		ev.Watchdog.Baseline = mustAtoi64(m[1])
		ev.Watchdog.Trigger = mustAtoi64(m[2])
		return ev, true
	}
	if m := reWdTrig.FindStringSubmatch(body); m != nil {
		ev.Kind = KindWatchdogTrigger
		ev.Watchdog.Active = mustAtoi64(m[1])
		ev.Watchdog.Trigger = mustAtoi64(m[2])
		return ev, true
	}
	if strings.HasPrefix(body, "starting ") {
		ev.Kind = KindStarting
		return ev, true
	}
	return ev, true
}

func mustAtoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
