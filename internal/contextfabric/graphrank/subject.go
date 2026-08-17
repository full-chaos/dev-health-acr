package graphrank

import (
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// NodeSubject reconstructs the canonical SubjectRef a node's projected
// attributes describe. Ported unchanged from zepgraph.nodeSubject.
func NodeSubject(node CandidateNode) (contextfabric.SubjectRef, bool) {
	kind := contextfabric.SubjectKind(StringAttribute(node.Attributes, "subject_kind"))
	canonicalID := strings.TrimSpace(StringAttribute(node.Attributes, "canonical_id"))
	label := strings.TrimSpace(StringAttribute(node.Attributes, "label"))
	if label == "" {
		label = strings.TrimSpace(node.Name)
	}
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: canonicalID, Label: label}
	if err := subject.Validate(); err != nil {
		return contextfabric.SubjectRef{}, false
	}
	return subject, true
}

// EvidenceRefs reads a node/edge's "evidence_refs" attribute, which every
// backend stores as a plain []string (never the authorization wildcard
// convention -- evidence refs have no "*" meaning).
func EvidenceRefs(attributes map[string]interface{}) []string {
	if refs, ok := attributes["evidence_refs"].([]string); ok {
		return refs
	}
	return nil
}

// IsObservationSubjectKind reports whether kind describes an observation
// about a canonical entity (a document or episode) rather than a
// first-class subject in its own right.
func IsObservationSubjectKind(kind contextfabric.SubjectKind) bool {
	return kind == contextfabric.SubjectDocument || kind == contextfabric.SubjectEpisode
}

// AliasAttributes reads a node's "aliases" attribute -- the bare-name (and
// other kind-native, e.g. ticket-key/native-key) identity handles a backend
// projected onto it (falkorgraph: propAliases, subjectMergeAttrs). Same
// convention as EvidenceRefs: a plain []string, never the authorization
// wildcard convention.
func AliasAttributes(attributes map[string]interface{}) []string {
	if aliases, ok := attributes["aliases"].([]string); ok {
		return aliases
	}
	return nil
}

// ProviderAliasAttributes reads a node's "provider_aliases" attribute
// (CHAOS-3884) -- provider-qualified identity variants (e.g.
// "github:full-chaos/dev-health-acr"), distinct from AliasAttributes'
// bare-name-shaped handles so NodeCandidate can tag MatchProviderKey
// separately from MatchAlias even though both flow through the identical
// projection/write/read path.
func ProviderAliasAttributes(attributes map[string]interface{}) []string {
	if aliases, ok := attributes["provider_aliases"].([]string); ok {
		return aliases
	}
	return nil
}

// IdentityNormalizationVersion names the CURRENT identity-term
// normalization definition NormalizeAliasTerm implements -- folded into
// contextfabric.ReuseKey/ReuseVersionAuthorities as a conjunctive answer-
// reuse dimension (CHAOS-3884) so a future tightening (e.g. adding NFC)
// invalidates rather than silently revalidates a stored answer produced
// under the old definition. Bump this STRING whenever NormalizeAliasTerm's
// actual transform changes, in the same commit.
const IdentityNormalizationVersion = "identity_norm_v1"

// NormalizeAliasTerm is identity_norm_v1 (CHAOS-3884, ratified for slice 1):
// TrimSpace + ToLower. Deliberately NOT NFC-normalizing yet -- the reviewer
// ruled NFC not mandatory over the measured, verified-ASCII alphabet this
// system's identity data actually uses (live-graph label/alias corpus:
// alphanumerics + "-./:_" only, no non-ASCII repo/project/team/ticket
// identifiers observed). That premise is MONITORED, not merely assumed --
// see MonitorIdentityTermAlphabet -- and IdentityNormalizationVersion
// (folded into ReuseKey) is what makes deferring NFC safe: a future
// tightening to NFC-then-lowercase changes this function's OUTPUT for any
// term containing a decomposed/precomposed form, and the version bump
// forces every stored answer keyed on the OLD normalization to miss reuse
// rather than silently revalidate under a definition that has changed.
//
// This is the ONLY place ToLower+TrimSpace is applied for identity
// matching/counting purposes -- projection-time alias/provider-alias
// writes, the identity reader's own match, and every counting map in
// resolve.go/resolution.go call THIS function, never re-derive the
// transform locally (CRITICAL-2's "one normal form, one implementation"
// requirement).
func NormalizeAliasTerm(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// IsASCIIIdentityTerm reports whether s contains only the alphabet
// NormalizeAliasTerm's soundness proof is scoped to (verified ASCII
// alphanumerics plus "-./:_" and space, matching the live-graph corpus
// checked when NFC was deferred). Used to MONITOR the premise rather than
// merely assume it: a caller (projection-time alias/provider-alias writes,
// devhealthsource) that finds a term failing this check should log/count
// it as a monitoring signal, not silently trust NormalizeAliasTerm's
// EqualFold-equivalence guarantee for it. Never used to reject or alter
// data -- a non-ASCII identity string still gets normalized and matched;
// it just does so outside this function's PROVEN alphabet, which is a
// fact worth surfacing, not enforcing.
func IsASCIIIdentityTerm(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '.' || r == '/' || r == ':' || r == '_' || r == ' ':
		default:
			return false
		}
	}
	return true
}

// aliasLookupScopedKinds is the GROWABLE counting-scope registry
// (CHAOS-3884, HIGH-5 / spot-check item "growable scope registry"): every
// kind whose Aliases/ProviderAliases the identity reader enumerates and
// counts claimants for, regardless of whether that kind may itself
// auto-commit via the identity fast path (see isAliasIdentityEligibleKind,
// a STRICT SUBSET). Deliberately a package-level var, not a hardcoded
// switch inline in isAliasLookupScopedKind, so a later slice widening
// coverage (the unified architecture's stated direction: label becomes a
// key class for EVERY kind, which is how the ci_pipeline_run/pull_request
// exactIndex truncation residual eventually closes) is a one-line addition
// here, not a scattered set of call-site edits. Today's four kinds are
// exactly what devhealthsource populates Aliases for as of this ticket:
// work_item (ticketKeyAlias), team/project (native_key/project_key), and
// repository (this ticket, bare-name/provider-variant).
var aliasLookupScopedKinds = map[contextfabric.SubjectKind]bool{
	contextfabric.SubjectRepository: true,
	contextfabric.SubjectProject:    true,
	contextfabric.SubjectTeam:       true,
	contextfabric.SubjectWorkItem:   true,
}

// isAliasLookupScopedKind reports whether kind is in the counting scope
// (aliasLookupScopedKinds) -- a claimant of this kind is discoverable and
// COUNTED toward collision detection, even though it may not itself be
// commit-eligible (see isAliasIdentityEligibleKind).
func isAliasLookupScopedKind(kind contextfabric.SubjectKind) bool {
	return aliasLookupScopedKinds[kind]
}

// IsAliasLookupScopedKind is the exported mirror of isAliasLookupScopedKind,
// existing so an identity-universe reader in another package (e.g.
// devhealthsource's IdentityUniverse) can cross-check its OWN source-table
// coverage against this package's counting scope, closing the loop a
// cross-package registry pair can otherwise silently drift apart on.
func IsAliasLookupScopedKind(kind contextfabric.SubjectKind) bool {
	return isAliasLookupScopedKind(kind)
}

// aliasIdentityEligibleKinds is the NARROW commit-eligibility allowlist
// (CHAOS-3884) -- a STRICT SUBSET of aliasLookupScopedKinds. Only these
// kinds may earn the confidence=1 identity bump and participate in the
// identityIndex fast-path/rescue-guard machinery (resolution.go). Team and
// work_item claimants are counted (isAliasLookupScopedKind) but never
// themselves eligible here -- their own alias uniqueness has not been
// separately argued the way the small, per-org-enumerable
// repository/project population has (see the design doc's "per-kind
// enumerability" discussion). Widening this list is a SEPARATE, explicit
// decision from widening aliasLookupScopedKinds -- do not conflate the two
// registries even though today one is a literal subset of the other.
var aliasIdentityEligibleKinds = map[contextfabric.SubjectKind]bool{
	contextfabric.SubjectRepository: true,
	contextfabric.SubjectProject:    true,
}

// isAliasIdentityEligibleKind reports whether kind may auto-commit via the
// identity mechanism (confidence=1 bump, identityIndex fast path, rescue
// guard). See aliasIdentityEligibleKinds' own doc comment for the scope
// argument and why it is a strict subset of isAliasLookupScopedKind.
func isAliasIdentityEligibleKind(kind contextfabric.SubjectKind) bool {
	return aliasIdentityEligibleKinds[kind]
}

// IsObservationAttributionRelation reports whether normalizedName (already
// run through NormalizeRelation) is one of the specific relation kinds a
// backend's content/episode projection uses to attach a document or episode
// to the canonical subject it is authoritatively about ("DOCUMENTED_BY",
// "HAS_EPISODE"). Traversal must not follow any other edge that happens to
// point at an observation node -- see zepgraph.isObservationAttributionRelation
// for the original rationale (a generic MENTIONS/REFERENCES relationship
// is a much weaker, not-necessarily-singular association).
func IsObservationAttributionRelation(normalizedName string) bool {
	switch normalizedName {
	case "DOCUMENTED_BY", "HAS_EPISODE":
		return true
	default:
		return false
	}
}
