package router

import "time"

type Model struct {
	ID string `json:"id"`
}

type OpenAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type OpenAIList struct {
	Object string        `json:"object"`
	Data   []OpenAIModel `json:"data"`
}

type Catalog struct {
	names []string
}

func NewCatalog(names []string) *Catalog { return &Catalog{names: names} }

func (c *Catalog) List() []Model {
	out := make([]Model, 0, len(c.names))
	for _, n := range c.names {
		out = append(out, Model{ID: n})
	}
	return out
}

func (c *Catalog) OpenAIResponse() OpenAIList {
	now := time.Now().Unix()
	models := c.List()
	data := make([]OpenAIModel, 0, len(models))
	for _, m := range models {
		data = append(data, OpenAIModel{ID: m.ID, Object: "model", Created: now, OwnedBy: "mlx-stack"})
	}
	return OpenAIList{Object: "list", Data: data}
}
