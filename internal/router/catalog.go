package router

import (
	"time"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

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
	cfg *config.Config
}

func NewCatalog(cfg *config.Config) *Catalog { return &Catalog{cfg: cfg} }

func (c *Catalog) List() []Model {
	out := []Model{}
	for name := range c.cfg.Chat.Profiles {
		out = append(out, Model{ID: name})
	}
	return out
}

func (c *Catalog) OpenAIResponse() OpenAIList {
	models := c.List()
	now := time.Now().Unix()
	data := make([]OpenAIModel, 0, len(models))
	for _, m := range models {
		data = append(data, OpenAIModel{ID: m.ID, Object: "model", Created: now, OwnedBy: "mlx-stack"})
	}
	return OpenAIList{Object: "list", Data: data}
}
