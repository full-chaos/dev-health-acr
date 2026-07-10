package contextpacket

import (
	"net/url"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type evidenceAdapter interface {
	safeURI(string) string
	structured(contractsv1.EvidenceRef) map[string]any
}

type providerEvidenceAdapter struct {
	structuredKey string
	hosts         map[string]struct{}
	expectedType  string
}

var catalogEvidenceAdapters = map[string]providerEvidenceAdapter{
	"repository_freshness.v1":           {structuredKey: "repository_id", expectedType: "repository"},
	"work_items.v1":                     {structuredKey: "work_item_id", expectedType: "work_item"},
	"work_item_dependencies.v1":         {structuredKey: "dependency_id", expectedType: "work_item_dependency"},
	"git_commits.v1":                    {structuredKey: "commit_sha", expectedType: "commit"},
	"git_commit_files.v1":               {structuredKey: "commit_file", expectedType: "commit_file"},
	"pull_requests.v1":                  {structuredKey: "pull_request_number", expectedType: "pull_request"},
	"pull_request_reviews.v1":           {structuredKey: "review_id", expectedType: "pull_request_review"},
	"ci_pipeline_runs.v1":               {structuredKey: "pipeline_run_id", expectedType: "ci_pipeline_run"},
	"work_graph.v1":                     {structuredKey: "edge_id", expectedType: "work_graph_edge"},
	"ai_workflow_runs.v1":               {structuredKey: "run_id", expectedType: "ai_workflow_run"},
	"ai_workflow_artifacts.v1":          {structuredKey: "artifact_id"},
	"ai_review_outcomes.v1":             {structuredKey: "review_outcome_id", expectedType: "review_outcome"},
	"deployments.v1":                    {structuredKey: "deployment_id", expectedType: "deployment"},
	"incidents.v1":                      {structuredKey: "incident_id", expectedType: "incident"},
	"deployment_incident_provenance.v1": {structuredKey: "edge_id", expectedType: "deployment_incident_edge"},
	"file_hotspots.v1":                  {structuredKey: "file_path", expectedType: "file_hotspot"},
	"file_complexity.v1":                {structuredKey: "file_path", expectedType: "file_complexity"},
}

func evidenceAdapterFor(evidence contractsv1.EvidenceRef) (evidenceAdapter, bool) {
	if evidence.SourceVersion != "" {
		adapter, ok := catalogEvidenceAdapters[evidence.SourceVersion]
		if !ok || (adapter.expectedType != "" && adapter.expectedType != evidence.Source.EntityType) {
			return nil, false
		}
		return adapter, true
	}
	switch evidence.Source.System {
	case "github_pr":
		return providerEvidenceAdapter{
			structuredKey: "pull_request_id",
			hosts:         providerHosts("github.com", "example.invalid"),
		}, true
	case "git":
		return providerEvidenceAdapter{
			structuredKey: "commit_sha",
			hosts:         providerHosts("github.com", "gitlab.com", "example.invalid"),
		}, true
	case "ci":
		return providerEvidenceAdapter{
			structuredKey: "check_run_id",
			hosts:         providerHosts("github.com", "gitlab.com", "app.circleci.com", "buildkite.com", "example.invalid"),
		}, true
	default:
		return nil, false
	}
}

func providerHosts(values ...string) map[string]struct{} {
	hosts := make(map[string]struct{}, len(values))
	for _, value := range values {
		hosts[value] = struct{}{}
	}
	return hosts
}

func (a providerEvidenceAdapter) structured(evidence contractsv1.EvidenceRef) map[string]any {
	return map[string]any{a.structuredKey: cleanEvidenceText(evidence.Source.EntityID, 1_024)}
}

func (a providerEvidenceAdapter) safeURI(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil {
		return ""
	}
	host := strings.ToLower(parsed.Host)
	if _, ok := a.hosts[host]; !ok {
		return ""
	}
	return (&url.URL{Scheme: "https", Host: host, Path: parsed.Path}).String()
}
