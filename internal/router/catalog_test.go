package router

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

func TestCatalog_AggregatesChatProfiles(t *testing.T) {
	cfg := &config.Config{
		Chat: config.Chat{
			Profiles: map[string]config.Profile{
				"valkyrie": {Model: "/m/v", Engine: "lm"},
				"scout":    {Model: "/m/s", Engine: "vlm"},
			},
		},
	}
	c := NewCatalog(cfg)
	out := c.List()
	ids := []string{}
	for _, m := range out {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	if len(ids) != 2 || ids[0] != "scout" || ids[1] != "valkyrie" {
		t.Errorf("got: %v", ids)
	}
}

func TestCatalog_IncludesTagsAlias(t *testing.T) {
	cfg := &config.Config{
		Chat: config.Chat{Profiles: map[string]config.Profile{"v": {Model: "/m", Engine: "lm"}}},
		Tags: config.Tags{Alias: "qwen-tags"},
	}
	c := NewCatalog(cfg)
	ids := []string{}
	for _, m := range c.List() {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	want := []string{"qwen-tags", "v"}
	if len(ids) != 2 || ids[0] != want[0] || ids[1] != want[1] {
		t.Errorf("got: %v", ids)
	}
}

func TestCatalog_JSONShape(t *testing.T) {
	cfg := &config.Config{Chat: config.Chat{Profiles: map[string]config.Profile{"p": {Model: "/m", Engine: "lm"}}}}
	c := NewCatalog(cfg)
	b, _ := json.Marshal(c.OpenAIResponse())
	var resp struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	json.Unmarshal(b, &resp)
	if resp.Object != "list" {
		t.Errorf("object: %q", resp.Object)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "p" || resp.Data[0].Object != "model" {
		t.Errorf("data: %+v", resp.Data)
	}
}
