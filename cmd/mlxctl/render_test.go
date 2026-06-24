package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderStatus_Basic(t *testing.T) {
	body := []byte(`{
		"backends": [
			{"name":"chat","group":"chat","mode":"swap","engine":"lm","model":"valkyrie","url":"http://x:1234","running":true,"pid":100,"current_name":"valkyrie"},
			{"name":"embed","group":"embed","mode":"persistent","engine":"embed","url":"http://x:1236","running":true,"pid":200}
		]
	}`)
	var b bytes.Buffer
	renderStatus(&b, body)
	out := b.String()
	if !strings.Contains(out, "chat") || !strings.Contains(out, "valkyrie") {
		t.Errorf("missing chat row: %s", out)
	}
	if !strings.Contains(out, "embed") {
		t.Errorf("missing embed row: %s", out)
	}
}

func TestRenderStatus_WedgedShowsUnhealthy(t *testing.T) {
	body := []byte(`{
		"backends": [
			{"name":"chat","group":"chat","mode":"swap","engine":"lm","url":"http://x:1234","running":false,"state":"unhealthy","pid":100,"current_name":"valkyrie"}
		]
	}`)
	var b bytes.Buffer
	renderStatus(&b, body)
	out := b.String()
	if !strings.Contains(out, "wedged") {
		t.Errorf("unhealthy backend should render as 'wedged', got: %s", out)
	}
}

func TestRenderStatus_ShowsTier(t *testing.T) {
	body := []byte(`{
		"backends": [
			{"name":"chat","group":"chat","mode":"swap","engine":"lm","url":"http://x:1234","running":true,"state":"ready","pid":100,"current_name":"valkyrie","tier":"offloaded"}
		]
	}`)
	var b bytes.Buffer
	renderStatus(&b, body)
	if !strings.Contains(b.String(), "offloaded") {
		t.Errorf("tier not rendered: %s", b.String())
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
