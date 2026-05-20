package router

import (
	"encoding/json"
	"sort"
	"testing"
)

func TestCatalog_FromNames(t *testing.T) {
	c := NewCatalog([]string{"valkyrie", "scout", "embed"})
	ids := []string{}
	for _, m := range c.List() {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	if len(ids) != 3 || ids[0] != "embed" || ids[1] != "scout" || ids[2] != "valkyrie" {
		t.Errorf("ids: %v", ids)
	}
}

func TestCatalog_OpenAIResponse(t *testing.T) {
	c := NewCatalog([]string{"x"})
	b, _ := json.Marshal(c.OpenAIResponse())
	var resp OpenAIList
	json.Unmarshal(b, &resp)
	if resp.Object != "list" || len(resp.Data) != 1 || resp.Data[0].ID != "x" || resp.Data[0].Object != "model" {
		t.Errorf("resp: %+v", resp)
	}
}
