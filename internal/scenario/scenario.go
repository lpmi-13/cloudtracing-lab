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

type AnswerKey struct {
	Service            string `json:"service"`
	Issue              string `json:"issue"`
	SpanOperation      string `json:"span_operation"`
	SpanAttributeKey   string `json:"span_attribute_key"`
	SpanAttributeValue string `json:"span_attribute_value"`
	AttributeKey       string `json:"attribute_key"`
	AttributeValue     string `json:"attribute_value"`
}

type BatchPlan struct {
	FaultyCount     int `json:"faulty_count"`
	HealthyCount    int `json:"healthy_count"`
	BeforeCount     int `json:"before_count"`
	AfterCount      int `json:"after_count"`
	CandidateCount  int `json:"candidate_count"`
	BackgroundCount int `json:"background_count"`
}

type Definition struct {
	ID               string               `json:"id"`
	Level            int                  `json:"level"`
	VariantGroup     string               `json:"variant_group"`
	AssessmentType   string               `json:"assessment_type"`
	AssessmentPrompt string               `json:"assessment_prompt"`
	Title            string               `json:"title"`
	Objective        string               `json:"objective"`
	Prompt           string               `json:"prompt"`
	Hint1            string               `json:"hint_1"`
	Hint2            string               `json:"hint_2"`
	Route            string               `json:"route"`
	TrafficPath      string               `json:"traffic_path"`
	FocusService     string               `json:"focus_service"`
	FocusOperation   string               `json:"focus_operation"`
	ExpectedService  string               `json:"expected_service"`
	ExpectedTier     string               `json:"expected_tier"`
	ExpectedIssue    string               `json:"expected_issue"`
	Answer           string               `json:"answer"`
	SearchLimit      int                  `json:"search_limit"`
	AnswerKey        AnswerKey            `json:"answer_key"`
	BatchPlan        BatchPlan            `json:"batch_plan"`
	Services         map[string]FaultSpec `json:"services"`
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
