package scenario

import (
	"encoding/json"
	"fmt"
	"os"
)

type FaultSpec struct {
	Mode       string `json:"mode"`
	LatencyMS  int    `json:"latency_ms"`
	Repeat     int    `json:"repeat"`
	QueryLabel string `json:"query_label"`
	QueryText  string `json:"query_text"`
}

type Definition struct {
	ID              string               `json:"id"`
	Title           string               `json:"title"`
	Objective       string               `json:"objective"`
	Prompt          string               `json:"prompt"`
	Hint1           string               `json:"hint_1"`
	Hint2           string               `json:"hint_2"`
	Route           string               `json:"route"`
	TrafficPath     string               `json:"traffic_path"`
	FocusService    string               `json:"focus_service"`
	FocusOperation  string               `json:"focus_operation"`
	ExpectedService string               `json:"expected_service"`
	ExpectedTier    string               `json:"expected_tier"`
	ExpectedIssue   string               `json:"expected_issue"`
	Answer          string               `json:"answer"`
	Services        map[string]FaultSpec `json:"services"`
}

func Load(path string) ([]Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read scenario file: %w", err)
	}

	var defs []Definition
	if err := json.Unmarshal(data, &defs); err != nil {
		return nil, fmt.Errorf("parse scenario file: %w", err)
	}

	return defs, nil
}

func Index(defs []Definition) map[string]Definition {
	index := make(map[string]Definition, len(defs))
	for _, def := range defs {
		index[def.ID] = def
	}
	return index
}

func Lookup(defs map[string]Definition, id string) (Definition, bool) {
	if id == "" {
		return Definition{}, false
	}

	def, ok := defs[id]
	return def, ok
}
