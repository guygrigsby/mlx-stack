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

func minChat() Chat {
	return Chat{
		DefaultProfile: "p1",
		Host:           "127.0.0.1",
		Port:           1234,
		Profiles:       map[string]Profile{"p1": {Model: "/tmp", Engine: "lm"}},
	}
}
