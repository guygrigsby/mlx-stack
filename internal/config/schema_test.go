package config

import (
	"strings"
	"testing"
)

func TestValidate_OK(t *testing.T) {
	c := &Config{
		LogDir:     "/tmp/logs",
		ModelsRoot: "/tmp/models",
		PythonBin:  "/usr/bin/python",
		Router: Router{Host: "127.0.0.1", Port: 1231, ExtraPorts: []int{}},
		Chat: Chat{
			DefaultProfile: "p1",
			Host:           "127.0.0.1",
			Port:           1234,
			SwapTimeoutSec: 30,
			Profiles: map[string]Profile{
				"p1": {Model: "/tmp/models/p1", Engine: "lm"},
			},
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_MissingPythonBin(t *testing.T) {
	c := &Config{Router: Router{Host: "127.0.0.1", Port: 1231}, Chat: minChat()}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "python_bin") {
		t.Fatalf("expected python_bin error, got: %v", err)
	}
}

func TestValidate_DefaultProfileMustExist(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1231},
		Chat: Chat{
			DefaultProfile: "ghost",
			Host:           "127.0.0.1",
			Port:           1234,
			Profiles:       map[string]Profile{"p1": {Model: "/tmp", Engine: "lm"}},
		},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "default_profile") {
		t.Fatalf("expected default_profile error, got: %v", err)
	}
}

func TestValidate_ProfileEngineMustBeLmOrVlm(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1231},
		Chat: Chat{
			DefaultProfile: "p1",
			Host:           "127.0.0.1",
			Port:           1234,
			Profiles:       map[string]Profile{"p1": {Model: "/tmp", Engine: "audio"}},
		},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "engine") {
		t.Fatalf("expected engine error, got: %v", err)
	}
}

func TestValidate_PortRange(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 0},
		Chat:      minChat(),
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "router.port") {
		t.Fatalf("expected router.port error, got: %v", err)
	}
}

func TestValidate_TagsOK(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1231},
		Chat:      minChat(),
		Tags: Tags{
			Host:   "127.0.0.1",
			Port:   1235,
			Model:  "/m/qwen-tags",
			Engine: "vlm",
			Alias:  "qwen-tags",
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidate_TagsZeroValueOK(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1231},
		Chat:      minChat(),
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("tags zero-value must validate: %v", err)
	}
}

func TestValidate_TagsBadEngine(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1231},
		Chat:      minChat(),
		Tags: Tags{
			Host:   "127.0.0.1",
			Port:   1235,
			Model:  "/m",
			Engine: "audio",
			Alias:  "x",
		},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "tags.engine") {
		t.Fatalf("want tags.engine error: %v", err)
	}
}

func TestValidate_TagsMissingAlias(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1231},
		Chat:      minChat(),
		Tags: Tags{
			Host:   "127.0.0.1",
			Port:   1235,
			Model:  "/m",
			Engine: "lm",
			Alias:  "",
		},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "tags.alias") {
		t.Fatalf("want tags.alias error: %v", err)
	}
}

func TestValidate_EmbedManaged(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1231},
		Chat:      minChat(),
		Embed:     Embed{Managed: true, Host: "127.0.0.1", Port: 1236, Model: "/m/e", Alias: "embed"},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidate_EmbedExternal(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1231},
		Chat:      minChat(),
		Embed:     Embed{Managed: false, URL: "http://other.local:1236", Alias: "embed"},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidate_EmbedZeroValue(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1231},
		Chat:      minChat(),
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("embed zero-value must validate: %v", err)
	}
}

func TestValidate_EmbedManagedMissingModel(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1231},
		Chat:      minChat(),
		Embed:     Embed{Managed: true, Port: 1236, Alias: "embed"},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "embed.model") {
		t.Fatalf("want embed.model error: %v", err)
	}
}

func TestValidate_EmbedExternalMissingURL(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1231},
		Chat:      minChat(),
		Embed:     Embed{Managed: false, Alias: "embed"},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "embed.url") {
		t.Fatalf("want embed.url error: %v", err)
	}
}

func minChat() Chat {
	return Chat{
		DefaultProfile: "p1",
		Host:           "127.0.0.1",
		Port:           1234,
		Profiles:       map[string]Profile{"p1": {Model: "/tmp", Engine: "lm"}},
	}
}
