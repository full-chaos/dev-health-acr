package contextpacket

import (
	"fmt"
	"sort"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func displayableEvidence(in []contractsv1.EvidenceRef) ([]contractsv1.EvidenceRef, []contractsv1.UnavailableSource) {
	out, missing := make([]contractsv1.EvidenceRef, 0, len(in)), []contractsv1.UnavailableSource{}
	for _, ref := range in {
		if ref.Availability == contractsv1.EvidenceAvailable || ref.Availability == contractsv1.EvidenceStale {
			out = append(out, ref)
		} else {
			missing = append(missing, contractsv1.UnavailableSource{Source: ref.Source.System, Reason: string(ref.Availability)})
		}
	}
	return out, missing
}
func requestedCategories(items []contractsv1.ContextPacketItem, requested []contractsv1.PacketCategory) []contractsv1.ContextPacketItem {
	if len(requested) == 0 {
		return items
	}
	allow := map[contractsv1.PacketCategory]bool{}
	for _, c := range requested {
		allow[c] = true
	}
	out := []contractsv1.ContextPacketItem{}
	for _, item := range items {
		if allow[item.Category] {
			out = append(out, item)
		}
	}
	return out
}
func setStatus(packet *contractsv1.ContextPacket, bundle storage.EvidenceBundle, candidateCount int) {
	if len(packet.Items) == 0 {
		if candidateCount > 0 {
			packet.Status = contractsv1.PacketPartial
			packet.Warnings = append(packet.Warnings, "context_filtered_or_truncated")
		} else if len(bundle.Unavailable) > 0 {
			packet.Status = contractsv1.PacketDegraded
		} else {
			packet.Status = contractsv1.PacketEmpty
			packet.Warnings = append(packet.Warnings, "no_evidence_found")
		}
		return
	}
	if len(bundle.Unavailable) > 0 || packet.Budget.Truncated {
		packet.Status = contractsv1.PacketPartial
	} else {
		packet.Status = contractsv1.PacketComplete
	}
}

func validateEvidenceBundle(evidence []contractsv1.EvidenceRef) error {
	for _, ref := range evidence {
		if err := validateEvidence(ref); err != nil {
			return fmt.Errorf("validate retrieved evidence: %w", err)
		}
	}
	return nil
}
func coverage(b storage.EvidenceBundle) contractsv1.Coverage {
	considered, available := []string{}, []string{}
	unavailable := append([]contractsv1.UnavailableSource{}, b.Unavailable...)
	for _, w := range b.Watermarks {
		considered = append(considered, w.Source)
		switch w.Status {
		case "fresh", "stale":
			available = append(available, w.Source)
		case "missing", "unavailable":
			unavailable = appendUnavailable(unavailable, w.Source, w.Status)
		}
	}
	for _, e := range b.Evidence {
		considered, available = append(considered, e.Source.System), append(available, e.Source.System)
	}
	for _, u := range unavailable {
		considered = append(considered, u.Source)
	}
	return contractsv1.Coverage{SourcesConsidered: sortedUnique(considered), SourcesAvailable: sortedUnique(available), SourcesUnavailable: sortedUnavailable(unavailable), Partial: len(unavailable) > 0, DegradedReasons: []string{}}
}

func appendUnavailable(values []contractsv1.UnavailableSource, source, reason string) []contractsv1.UnavailableSource {
	for _, value := range values {
		if value.Source == source {
			return values
		}
	}
	return append(values, contractsv1.UnavailableSource{Source: source, Reason: reason})
}
func warnings(b storage.EvidenceBundle) []string {
	out := []string{}
	for _, w := range b.Watermarks {
		if w.Status == "stale" {
			out = append(out, "source_stale:"+w.Source)
		}
	}
	for _, u := range b.Unavailable {
		out = append(out, "source_unavailable:"+u.Source+":"+u.Reason)
	}
	return sortedUnique(out)
}
func sortedWatermarks(in []contractsv1.SourceWatermark) []contractsv1.SourceWatermark {
	out := append(make([]contractsv1.SourceWatermark, 0, len(in)), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}
func sortedUnavailable(in []contractsv1.UnavailableSource) []contractsv1.UnavailableSource {
	out := append(make([]contractsv1.UnavailableSource, 0, len(in)), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source == out[j].Source {
			return out[i].Reason < out[j].Reason
		}
		return out[i].Source < out[j].Source
	})
	return out
}
func sortedUnique(in []string) []string {
	set := map[string]bool{}
	for _, v := range in {
		if v != "" {
			set[v] = true
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func validatePacket(p contractsv1.ContextPacket) error {
	if p.SchemaVersion != contractsv1.ContextPacketSchema || p.ContextPacketID == "" || p.ResolvedScope.RepoID == "" {
		return fmt.Errorf("invalid context packet identity")
	}
	if err := validatePacketOutputBounds(p); err != nil {
		return err
	}
	for _, item := range p.Items {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("validate item: %w", err)
		}
	}
	if p.Budget.ItemsUsed != len(p.Items) || p.Budget.SerializedBytes > p.Budget.MaxSerializedBytes || p.Budget.EstimatedTokens > p.Budget.MaxOutputTokens {
		return fmt.Errorf("invalid context packet budget")
	}
	return nil
}

func ensurePacketArrays(packet *contractsv1.ContextPacket) {
	if packet.Items == nil {
		packet.Items = []contractsv1.ContextPacketItem{}
	}
	if packet.RequiredChecks == nil {
		packet.RequiredChecks = []contractsv1.RequiredCheck{}
	}
	if packet.RecommendedNextSteps == nil {
		packet.RecommendedNextSteps = []contractsv1.RecommendedStep{}
	}
	if packet.ResolvedScope.FallbackReasons == nil {
		packet.ResolvedScope.FallbackReasons = []string{}
	}
	if packet.Freshness.Watermarks == nil {
		packet.Freshness.Watermarks = []contractsv1.SourceWatermark{}
	}
	if packet.Coverage.SourcesConsidered == nil {
		packet.Coverage.SourcesConsidered = []string{}
	}
	if packet.Coverage.SourcesAvailable == nil {
		packet.Coverage.SourcesAvailable = []string{}
	}
	if packet.Coverage.SourcesUnavailable == nil {
		packet.Coverage.SourcesUnavailable = []contractsv1.UnavailableSource{}
	}
	if packet.Coverage.DegradedReasons == nil {
		packet.Coverage.DegradedReasons = []string{}
	}
	if packet.Warnings == nil {
		packet.Warnings = []string{}
	}
}
