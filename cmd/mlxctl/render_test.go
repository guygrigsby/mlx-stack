package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderStatus_Basic(t *testing.T) {
	body := []byte(`{
		"chat": {"current_profile": "valkyrie", "pid": 12345, "url": "http://127.0.0.1:1234", "profiles": ["valkyrie","scout"]},
		"workers": {
			"chat[\"valkyrie\"]": {"name": "chat[\"valkyrie\"]", "latest_mem": {"active": 1073741824, "cache": 0, "peak": 2147483648}}
		}
	}`)
	var b bytes.Buffer
	renderStatus(&b, body)
	out := b.String()
	if !strings.Contains(out, "valkyrie") {
		t.Errorf("missing profile in output: %s", out)
	}
	if !strings.Contains(out, "12345") {
		t.Errorf("missing pid: %s", out)
	}
	if !strings.Contains(out, "1.0G") {
		t.Errorf("missing humanized mem: %s", out)
	}
}

func TestRenderStatus_NoChat(t *testing.T) {
	body := []byte(`{"chat":{"current_profile":"","pid":0,"url":"","profiles":["v"]}}`)
	var b bytes.Buffer
	renderStatus(&b, body)
	out := b.String()
	if !strings.Contains(out, "(stopped)") {
		t.Errorf("expected stopped indicator: %s", out)
	}
}

func TestRenderStatus_WithTags(t *testing.T) {
	body := []byte(`{
		"chat": {"current_profile": "v", "pid": 1, "url": "x", "profiles": ["v"]},
		"tags": {"alias": "qwen-tags", "pid": 999, "url": "http://x:1235", "running": true}
	}`)
	var b bytes.Buffer
	renderStatus(&b, body)
	out := b.String()
	if !strings.Contains(out, "qwen-tags") || !strings.Contains(out, "999") {
		t.Errorf("missing tags row: %s", out)
	}
}

func TestRenderStatus_WithTiming(t *testing.T) {
	body := []byte(`{
		"chat": {"current_profile": "scout", "pid": 42, "url": "http://127.0.0.1:9000", "profiles": ["scout"]},
		"workers": {
			"chat[\"scout\"]": {
				"name": "chat[\"scout\"]",
				"latest_mem": {"active": 536870912, "cache": 268435456, "peak": 1073741824},
				"latest_timing": {
					"RequestID": "req-abc",
					"PromptTokens": 100,
					"PrefillMs": 120.5,
					"PrefillTPS": 830.2,
					"DecodeMs": 450.0,
					"DecodeTPS": 42.1
				}
			}
		}
	}`)
	var b bytes.Buffer
	renderStatus(&b, body)
	out := b.String()
	if !strings.Contains(out, "req=req-abc") {
		t.Errorf("missing request ID in timing: %s", out)
	}
	if !strings.Contains(out, "512.0M") {
		t.Errorf("missing humanized active mem: %s", out)
	}
}

func TestRenderStatus_OtherWorkers(t *testing.T) {
	body := []byte(`{
		"chat": {"current_profile": "v", "pid": 1, "url": "x", "profiles": ["v"]},
		"workers": {
			"embed": {"name": "embed", "latest_mem": {"active": 104857600, "cache": 0, "peak": 209715200}},
			"tts":   {"name": "tts"}
		}
	}`)
	var b bytes.Buffer
	renderStatus(&b, body)
	out := b.String()
	if !strings.Contains(out, "embed") {
		t.Errorf("missing embed worker: %s", out)
	}
	if !strings.Contains(out, "tts") {
		t.Errorf("missing tts worker: %s", out)
	}
	if !strings.Contains(out, "100.0M") {
		t.Errorf("missing humanized embed mem: %s", out)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1.0K"},
		{1536, "1.5K"},
		{1048576, "1.0M"},
		{1073741824, "1.0G"},
		{2147483648, "2.0G"},
	}
	for _, c := range cases {
		got := humanBytes(c.in)
		if got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
