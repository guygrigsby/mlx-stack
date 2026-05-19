package router

import (
	"encoding/json"
	"fmt"
)

func ExtractModel(body []byte) (string, error) {
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return "", fmt.Errorf("parse body: %w", err)
	}
	if probe.Model == "" {
		return "", fmt.Errorf("model field missing")
	}
	return probe.Model, nil
}

func RewriteModel(body []byte, newModel string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse body: %w", err)
	}
	enc, _ := json.Marshal(newModel)
	m["model"] = enc
	return json.Marshal(m)
}
