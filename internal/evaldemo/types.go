// Package evaldemo runs the public ACR evaluation fixture as a deterministic
// cold-plan versus packet-context comparison. It does not call an LLM.
package evaldemo

import "errors"

const SchemaVersion = "acr_evaluation_demo.v1"

var ErrInvalidated = errors.New("evaldemo: evaluation input invalidated")

type Scenario string

const (
	ScenarioDefault        Scenario = "default"
	ScenarioCorruptHash    Scenario = "corrupt-hash"
	ScenarioEmptyEvidence  Scenario = "empty-evidence"
	ScenarioMismatchedTask Scenario = "mismatched-task"
)

type Config struct {
	CorpusDir string
	Repeat    int
	Scenario  Scenario
}

type ResultStatus string

const (
	ResultComplete    ResultStatus = "complete"
	ResultInvalidated ResultStatus = "invalidated"
)

type Result struct {
	SchemaVersion string        `json:"schema_version"`
	Result        ResultStatus  `json:"result"`
	Disclosure    string        `json:"disclosure"`
	Fixture       Fixture       `json:"fixture"`
	Repeats       []Repeat      `json:"repeats"`
	Metrics       *Metrics      `json:"metrics,omitempty"`
	Invalidation  *Invalidation `json:"invalidation,omitempty"`
}

type Fixture struct {
	CorpusVersion string `json:"corpus_version"`
	ScenarioID    string `json:"scenario_id"`
	Repository    string `json:"repository"`
}

type Repeat struct {
	Index int       `json:"index"`
	Tasks []TaskRun `json:"tasks"`
}

type TaskRun struct {
	TaskID              string     `json:"task_id"`
	InputHash           string     `json:"input_hash"`
	ExpectedEvidenceIDs []string   `json:"expected_evidence_ids"`
	Cold                ColdRun    `json:"cold"`
	Context             ContextRun `json:"context"`
}

type ColdRun struct {
	PlanStatus string   `json:"plan_status"`
	Claims     []string `json:"claims"`
}

type ContextRun struct {
	PacketStatus    string     `json:"packet_status"`
	ContextPacketID string     `json:"context_packet_id"`
	Citations       []Citation `json:"citations"`
	EstimatedTokens int        `json:"estimated_tokens"`
}

type Citation struct {
	EvidenceRefID string `json:"evidence_ref_id"`
	EntityID      string `json:"entity_id"`
	SafeURI       string `json:"safe_uri"`
	Excerpt       string `json:"excerpt"`
}

type Metrics struct {
	Cold    SurfaceMetrics `json:"cold"`
	Context SurfaceMetrics `json:"context"`
}

type SurfaceMetrics struct {
	FactualErrorRate   RatioMetric `json:"factual_error_rate"`
	FileTestRecall     RatioMetric `json:"file_test_recall"`
	CitationPrecision  RatioMetric `json:"citation_precision"`
	IrrelevantItemRate RatioMetric `json:"irrelevant_item_rate"`
	PlanLatency        CostMetric  `json:"plan_latency"`
	TokenCost          CostMetric  `json:"token_cost"`
}

type RatioMetric struct {
	Numerator   int      `json:"numerator"`
	Denominator int      `json:"denominator"`
	Value       *float64 `json:"value"`
	Unit        string   `json:"unit"`
	Note        string   `json:"note,omitempty"`
}

type CostMetric struct {
	Value int    `json:"value"`
	Unit  string `json:"unit"`
	Note  string `json:"note,omitempty"`
}

type Invalidation struct {
	Scenario Scenario `json:"scenario"`
	Reason   string   `json:"reason"`
}
