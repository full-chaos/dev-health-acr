package v1

import (
	"sort"
	"strconv"
)

// The ITEM LEDGER: an occurrence-level account of what a Context Fabric answer
// actually charged, reconciled against the two totals this package already
// publishes, and the only place a result may be CERTIFIED as fitting.
//
// WHY A THIRD FUNCTION OVER THE SAME DOCUMENT, when this file's neighbour
// already counts (CountContextFabricResultItems) and already splits
// (AttributeContextFabricResultItems) it. Because the identity those two hold --
// Attribution.Total() == Counts.Budgeted() -- is shipped, is pinned by
// TestEveryChargedItemIsAttributedToExactlyOneBucket, and did not prevent the
// defect it looks like it should prevent. An accurate split can record 34 == 34
// against a ceiling of 30, and it can preserve the total while an item is
// attributed to the wrong group. Conservation is not capacity, and it is not
// per-occurrence correctness.
//
// So this adds the two guarantees a total cannot give:
//
//   - OCCURRENCE-LEVEL accounting. One debit per charged occurrence, carrying
//     which collection it came from and where in that collection it sat.
//     Dropping one member row while double-charging one driver leaves every
//     total unchanged and is visible here as a per-collection disagreement.
//   - A CAPACITY CERTIFICATE that cannot be constructed for an unreconciled
//     ledger. "A certified fitting result containing an item with no debit" is
//     refused by the type rather than by a convention someone has to remember.
//
// It MEASURES, like everything else in this file's neighbourhood: no
// truncation, no limitation, no coverage mutation, no refusal. What to do about
// a disagreement or an overdraw is the engine's decision (internal/contextfabric).

// ContextFabricChargedCollection is the CLOSED vocabulary of the result
// collections the item budget charges.
//
// It is an ALLOW-LIST with one member per charged collection, never a deny-list
// with an "else": a deny-list admits the next collection, and the zero value,
// by default. The census in ContextFabricResultItemCounts is the authority for
// WHAT is charged; this vocabulary names those collections so a debit can say
// which one it came from, and so a census field with no walker is a test
// failure rather than a silent gap.
type ContextFabricChargedCollection string

const (
	// ContextFabricChargedCandidates are SubjectResolution.Candidates.
	ContextFabricChargedCandidates ContextFabricChargedCollection = "candidates"
	// ContextFabricChargedCohortMembers are the cohort member ROWS. They are
	// a MANDATORY charge -- the answer cannot decline to carry them once a
	// cohort is admitted -- which is why an allocator must commit capacity
	// for them before it publishes any discretionary grant.
	ContextFabricChargedCohortMembers ContextFabricChargedCollection = "cohort_members"
	// ContextFabricChargedDrivers are Drivers.
	ContextFabricChargedDrivers ContextFabricChargedCollection = "drivers"
	// ContextFabricChargedRemainingWork are RemainingWork findings.
	ContextFabricChargedRemainingWork ContextFabricChargedCollection = "remaining_work"
	// ContextFabricChargedReadinessGaps are ReadinessGaps findings.
	ContextFabricChargedReadinessGaps ContextFabricChargedCollection = "readiness_gaps"
	// ContextFabricChargedConflicts are Conflicts findings.
	ContextFabricChargedConflicts ContextFabricChargedCollection = "conflicts"
	// ContextFabricChargedClaimedFacts are ClaimedFacts.
	ContextFabricChargedClaimedFacts ContextFabricChargedCollection = "claimed_facts"
)

var contextFabricChargedCollections = [7]ContextFabricChargedCollection{
	ContextFabricChargedCandidates,
	ContextFabricChargedCohortMembers,
	ContextFabricChargedDrivers,
	ContextFabricChargedRemainingWork,
	ContextFabricChargedReadinessGaps,
	ContextFabricChargedConflicts,
	ContextFabricChargedClaimedFacts,
}

// ContextFabricChargedCollectionCount is the closed vocabulary's size.
const ContextFabricChargedCollectionCount = len(contextFabricChargedCollections)

// ContextFabricChargedCollectionVocabulary returns the closed vocabulary in
// published order, as an ARRAY so the value is copied to the caller.
func ContextFabricChargedCollectionVocabulary() [ContextFabricChargedCollectionCount]ContextFabricChargedCollection {
	return contextFabricChargedCollections
}

// ValidContextFabricChargedCollection reports membership. The empty value is
// not a member.
func ValidContextFabricChargedCollection(value ContextFabricChargedCollection) bool {
	for _, member := range contextFabricChargedCollections {
		if member == value {
			return true
		}
	}
	return false
}

// ContextFabricItemDebit is ONE charged occurrence.
//
// Collection and Ordinal together are the OCCURRENCE KEY: they say which item
// this debit is, not merely how many there were. That is the whole difference
// between this and a total, and it is what makes "one row dropped, one driver
// charged twice" a visible disagreement instead of an unchanged sum.
type ContextFabricItemDebit struct {
	// Collection is which charged collection this occurrence came from.
	Collection ContextFabricChargedCollection `json:"collection"`
	// Ordinal is the occurrence's index within that collection, zero-based.
	Ordinal int `json:"ordinal"`
	// DeclaredID is the id the answer itself gives this item -- driver_id,
	// finding_id, claim_id, receipt_id -- or, for the cohort member rows
	// which carry none, the subject key.
	//
	// DIAGNOSIS ONLY. It is deliberately NOT the occurrence key and is
	// deliberately not checked for uniqueness: a producer that repeats an id
	// must still be counted twice here rather than collapsing two charged
	// items into one, and refusing the answer over it would turn a producer
	// defect into a server error -- see the note below the reconciler.
	DeclaredID string `json:"declared_id"`
	// Bucket is WHAT this item is about, carried from
	// contextFabricSubjectsBucket rather than re-derived. Two derivations of
	// one concept is the drift this whole file exists to refuse.
	Bucket ContextFabricItemBucket `json:"bucket"`
	// GroupIncidence is the DISTINCT groups this item names, and it is a
	// DIFFERENT UNIT from the debit above: an item naming two groups is ONE
	// physical debit and TWO incidences. Summing incidences as debits is the
	// mistake that let eighteen drivers naming two groups read as compliant
	// against an aggregate capacity, so the two units never share a field.
	//
	// Empty for an item naming no group, and for the collections whose items
	// are funded from a member or global grant rather than a group one.
	GroupIncidence []string `json:"group_incidence,omitempty"`
}

// ContextFabricLedgerStatus is the CLOSED vocabulary of what reconciliation
// found. It reaches telemetry and it decides whether a result may be certified,
// so it is spelled rather than left to a bare bool.
type ContextFabricLedgerStatus string

const (
	// ContextFabricLedgerReconciled: every charged occurrence has exactly one
	// debit, the per-collection debits match the census field by field, the
	// per-bucket debits match the published split, and the debit count equals
	// Budgeted().
	ContextFabricLedgerReconciled ContextFabricLedgerStatus = "reconciled"
	// ContextFabricLedgerCollectionDisagreement: a collection's debits and
	// its census count differ. This is the shape that survives a matching
	// total -- one row missing here, one item charged twice there.
	ContextFabricLedgerCollectionDisagreement ContextFabricLedgerStatus = "collection_count_disagreement"
	// ContextFabricLedgerBucketDisagreement: the per-bucket debit split and
	// AttributeContextFabricResultItems disagree, so the ledger and the
	// published attribution describe the same answer differently.
	ContextFabricLedgerBucketDisagreement ContextFabricLedgerStatus = "bucket_split_disagreement"
	// ContextFabricLedgerDuplicateOccurrence: a collection's occurrence keys
	// are not the dense range 0..n-1 -- an ordinal repeated, or one missing.
	// That is one occurrence dropped and another charged twice, which is the
	// shape every total in this package survives unchanged.
	ContextFabricLedgerDuplicateOccurrence ContextFabricLedgerStatus = "duplicate_occurrence"
	// ContextFabricLedgerInvalidDebit: a debit names a collection or a
	// bucket outside its closed vocabulary. The ledger's fields are
	// exported, so without this a forged debit -- a "paths" collection, a
	// bucket that is not a bucket -- contributes to the total, escapes the
	// per-collection comparison (which only walks KNOWN collections) and
	// can be handed a capacity certificate.
	ContextFabricLedgerInvalidDebit ContextFabricLedgerStatus = "invalid_debit"
)

// WHAT IS DELIBERATELY NOT A MEMBER: a "total disagreement". Once every
// collection's debits equal its census count, len(Debits) == Budgeted()
// FOLLOWS -- Budgeted() is the sum of exactly those census fields. A status
// that can never be reached is not a diagnosis, it is a token a dashboard will
// filter on forever and never see, so the property is asserted directly in the
// tests instead of published as an unreachable member.
var contextFabricLedgerStatuses = [5]ContextFabricLedgerStatus{
	ContextFabricLedgerReconciled,
	ContextFabricLedgerCollectionDisagreement,
	ContextFabricLedgerBucketDisagreement,
	ContextFabricLedgerDuplicateOccurrence,
	ContextFabricLedgerInvalidDebit,
}

// ContextFabricLedgerStatusCount is the closed vocabulary's size.
const ContextFabricLedgerStatusCount = len(contextFabricLedgerStatuses)

// ContextFabricLedgerStatusVocabulary returns it in published order.
func ContextFabricLedgerStatusVocabulary() [ContextFabricLedgerStatusCount]ContextFabricLedgerStatus {
	return contextFabricLedgerStatuses
}

// ValidContextFabricLedgerStatus reports membership; the empty value is not a
// member, so an unset status can never read as "reconciled".
func ValidContextFabricLedgerStatus(value ContextFabricLedgerStatus) bool {
	for _, member := range contextFabricLedgerStatuses {
		if member == value {
			return true
		}
	}
	return false
}

// ContextFabricItemLedger is the reconciled account of ONE result.
type ContextFabricItemLedger struct {
	// Debits is one entry per charged occurrence, in vocabulary order.
	Debits []ContextFabricItemDebit `json:"debits"`
	// Counts is the INDEPENDENT per-collection census, from the function the
	// budget itself uses. It is carried rather than recomputed by readers so
	// a disagreement is a fact recorded here rather than a comparison every
	// caller has to remember to make.
	Counts ContextFabricResultItemCounts `json:"counts"`
	// Attribution is the bucket split from the shipped function.
	//
	// The guarantee is NARROWER than "two independent derivations", and
	// saying so precisely matters: this walk and
	// AttributeContextFabricResultItems both classify through
	// contextFabricSubjectsBucket, because the ownership rule requires this
	// layer to CARRY that decision rather than re-derive it. So comparing
	// them catches ledger mutation and any future divergence between the
	// walk and the summary -- it does NOT catch a bug inside the shared
	// classifier, which is covered by that function's own direct tests.
	Attribution ContextFabricItemAttribution `json:"attribution"`
	// Status is what reconciliation found.
	Status ContextFabricLedgerStatus `json:"status"`
	// Disagreement NAMES the collection or bucket that disagreed, empty when
	// reconciled. A status without a name sends an operator back to the
	// source; this is the field that makes the defect diagnosable from the
	// artifact.
	Disagreement string `json:"disagreement,omitempty"`

	// RepeatedDeclaredIDs names every id that appears on more than one debit
	// of the same collection, in vocabulary-then-id order.
	//
	// AN OBSERVATION, NOT A DEFECT, and the distinction was paid for. An
	// earlier revision made a repeated id an accounting defect, and a real
	// engine fixture -- two claimed facts sharing one id -- became a 500 on
	// an account that balanced. But dropping the check entirely lost a
	// genuine signal: the occurrence key proves one debit per position and
	// says nothing about whether two positions hold the SAME source item.
	//
	// So identity is observed and reported, and the account stays
	// reconciled. Enforcement, if the answer contract wants any, belongs to
	// validation, which is where id uniqueness is already decided.
	RepeatedDeclaredIDs []string `json:"repeated_declared_ids,omitempty"`

	// produced records that this ledger came out of
	// ReconcileContextFabricResultItems, over a real result.
	//
	// UNEXPORTED, and it is the difference between "these numbers are
	// self-consistent" and "these numbers describe an answer". A zero
	// ContextFabricItemLedger is perfectly self-consistent -- no debits, no
	// counts, no attribution, everything agreeing at zero -- so a
	// re-derivation alone hands it a capacity certificate for any positive
	// ceiling. It is bound to no result at all. Only the reconciler sets
	// this, so outside this package a certificate cannot be obtained for a
	// ledger that was never reconciled against a document.
	produced bool

	// declaredGroups are the cohort's group keys in declared order.
	//
	// UNEXPORTED, because it is not part of the account -- it is the domain
	// of GroupIncidenceCounts, which must report a declared group that
	// nothing named as a measured ZERO rather than an absent key. A group
	// with an allowance and no usage is a fact; a missing map entry is a
	// question. The whole class-B history of this seam is zeros that meant
	// "never computed" being read as zeros that meant "measured none".
	declaredGroups []string
}

// Total is the number of debits: the charged size of the answer as this ledger
// accounts for it. Defined to equal Counts.Budgeted() when Status is
// reconciled, and deliberately readable when it is not -- an operator needs
// both numbers to see which way a disagreement went.
func (l ContextFabricItemLedger) Total() int { return len(l.Debits) }

// Reconciled is Status's boolean form for the common call site.
func (l ContextFabricItemLedger) Reconciled() bool {
	return l.Status == ContextFabricLedgerReconciled
}

// GroupIncidenceCounts is, per group key, how many charged items NAME that
// group -- an item naming several groups counted once for each, per the
// every_group rule the allocator declares.
//
// The sum of these counts may EXCEED Total(), and that is correct rather than a
// bug: incidence and physical cost are different units. A caller that adds
// these up and compares the sum to a ceiling has priced participation as if it
// were consumption, which is the false-overrun this separation exists to
// prevent.
//
// Groups the cohort declares but nothing names are present with a count of
// zero, so a group with a quota and no usage is a measured zero rather than an
// absent key.
func (l ContextFabricItemLedger) GroupIncidenceCounts() map[string]int {
	counts := map[string]int{}
	for _, group := range l.declaredGroups {
		counts[group] = 0
	}
	for _, debit := range l.Debits {
		for _, group := range debit.GroupIncidence {
			counts[group]++
		}
	}
	return counts
}

// ReconcileContextFabricResultItems walks the REAL result once and produces its
// occurrence-level ledger.
//
// THE INPUT IS THE RESULT, never a plan and never a projection. Everything this
// seam has failed at came from checking a document other than the one that gets
// served: a plan whose numbers add up, an earlier snapshot of a result a later
// composer then changed. A ledger is only worth having if it accounts for the
// document in hand.
func ReconcileContextFabricResultItems(result ContextFabricInvestigationResult) ContextFabricItemLedger {
	members := map[string]struct{}{}
	groups := map[string]struct{}{}
	declaredGroups := []string{}
	if result.Cohort != nil {
		for _, member := range result.Cohort.Members {
			members[contextFabricSubjectBucketKey(member.Subject)] = struct{}{}
		}
		for _, group := range result.Cohort.Groups {
			key := contextFabricSubjectBucketKey(group.Subject)
			if _, seen := groups[key]; !seen {
				groups[key] = struct{}{}
				declaredGroups = append(declaredGroups, key)
			}
		}
	}

	ledger := ContextFabricItemLedger{
		Counts:      CountContextFabricResultItems(result),
		Attribution: AttributeContextFabricResultItems(result),
	}

	// Resolution candidates: global by definition, exactly as attribution
	// charges them. They are alternatives the investigation did not commit
	// to, so they name no group and fund from no group's allowance.
	for ordinal, candidate := range result.SubjectResolution.Candidates {
		ledger.Debits = append(ledger.Debits, ContextFabricItemDebit{
			Collection: ContextFabricChargedCandidates,
			Ordinal:    ordinal,
			// The RECEIPT id, which the result validator already requires
			// to be unique across candidates
			// (validate_context_fabric_result.go:62-65). An earlier
			// revision keyed these on the subject and excluded them from
			// the duplicate check on the belief that nothing promised
			// candidate uniqueness. Something does, and excluding them
			// left the one collection where a dropped occurrence paired
			// with a duplicated one reconciled.
			DeclaredID: candidate.ReceiptID,
			Bucket:     ContextFabricItemBucketGlobal,
		})
	}
	// Cohort member ROWS: member-attributed by definition, and the charge an
	// allocator must commit before it grants anything discretionary.
	if result.Cohort != nil {
		for ordinal, member := range result.Cohort.Members {
			ledger.Debits = append(ledger.Debits, ContextFabricItemDebit{
				Collection: ContextFabricChargedCohortMembers,
				Ordinal:    ordinal,
				DeclaredID: contextFabricSubjectBucketKey(member.Subject),
				Bucket:     ContextFabricItemBucketMember,
			})
		}
	}
	for ordinal, driver := range result.Drivers {
		ledger.Debits = append(ledger.Debits, contextFabricSubjectsDebit(
			ContextFabricChargedDrivers, ordinal, driver.DriverID, driver.AffectedSubjects, members, groups))
	}
	for _, findings := range []struct {
		collection ContextFabricChargedCollection
		rows       []ContextFabricFinding
	}{
		{ContextFabricChargedRemainingWork, result.RemainingWork},
		{ContextFabricChargedReadinessGaps, result.ReadinessGaps},
		{ContextFabricChargedConflicts, result.Conflicts},
	} {
		for ordinal, finding := range findings.rows {
			ledger.Debits = append(ledger.Debits, contextFabricSubjectsDebit(
				findings.collection, ordinal, finding.FindingID, finding.Subjects, members, groups))
		}
	}
	for ordinal, claim := range result.ClaimedFacts {
		ledger.Debits = append(ledger.Debits, contextFabricSubjectsDebit(
			ContextFabricChargedClaimedFacts, ordinal, claim.ClaimID,
			[]ContextFabricSubjectRef{claim.Subject}, members, groups))
	}

	// Every declared group is present in the incidence map even when nothing
	// names it, so a group with an allowance and no usage reads as a
	// measured zero. Carried on the ledger rather than left to the reader,
	// because the reader is the one that would otherwise have to remember.
	ledger.declaredGroups = declaredGroups
	ledger.produced = true
	ledger.RepeatedDeclaredIDs = contextFabricRepeatedDeclaredIDs(ledger.Debits)
	ledger.Status, ledger.Disagreement = contextFabricReconcile(ledger)
	return ledger
}

// contextFabricSubjectsDebit builds one debit from an item's named subjects,
// taking the BUCKET from the shipped precedence function and the INCIDENCE from
// the groups the item actually names. Two units, one walk, no second authority
// over either.
func contextFabricSubjectsDebit(
	collection ContextFabricChargedCollection,
	ordinal int,
	declaredID string,
	subjects []ContextFabricSubjectRef,
	members, groups map[string]struct{},
) ContextFabricItemDebit {
	debit := ContextFabricItemDebit{
		Collection: collection,
		Ordinal:    ordinal,
		DeclaredID: declaredID,
		Bucket:     contextFabricSubjectsBucket(subjects, members, groups),
	}
	named := map[string]struct{}{}
	for _, subject := range subjects {
		key := contextFabricSubjectBucketKey(subject)
		if _, isGroup := groups[key]; !isGroup {
			continue
		}
		if _, seen := named[key]; seen {
			// DISTINCT groups. An item naming one group twice participates
			// in it once, exactly as the bucket precedence refuses to
			// promote such an item to multi_group.
			continue
		}
		named[key] = struct{}{}
		debit.GroupIncidence = append(debit.GroupIncidence, key)
	}
	return debit
}

// contextFabricReconcile is the accounting check: three derivations of the same
// answer, compared.
//
// NOT "three INDEPENDENT derivations", and the precision is the point. The debit
// walk and AttributeContextFabricResultItems both classify through
// contextFabricSubjectsBucket, because the ownership rule requires this layer to
// CARRY that decision rather than re-derive it. So the bucket comparison detects
// ledger mutation and any future divergence between the walk and the summary --
// it cannot detect a bug INSIDE the shared classifier, which is covered by that
// function's own direct tests. The per-collection comparison shares
// CountContextFabricResultItems in the same way.
//
// ORDER MATTERS and is stated rather than incidental. Vocabulary validity comes
// first, because a debit naming a collection nothing knows about cannot be
// compared to anything -- it would contribute to the total and escape every
// per-collection comparison, which walks only KNOWN collections. Then the
// occurrence keys, which say WHICH items exist; then the counts, which say how
// many; then the bucket split.
func contextFabricReconcile(ledger ContextFabricItemLedger) (ContextFabricLedgerStatus, string) {
	ordinals := map[ContextFabricChargedCollection]map[int]struct{}{}
	perCollection := map[ContextFabricChargedCollection]int{}
	perBucket := ContextFabricItemAttribution{}
	for _, debit := range ledger.Debits {
		// FAIL CLOSED on a debit outside either closed vocabulary. The
		// ledger's fields are exported, so this is what stops a forged
		// debit -- a "paths" collection, a bucket that is not a bucket --
		// from being counted, escaping the comparisons and collecting a
		// capacity certificate.
		if !ValidContextFabricChargedCollection(debit.Collection) {
			return ContextFabricLedgerInvalidDebit, "collection:" + string(debit.Collection)
		}
		if !ValidContextFabricItemBucket(debit.Bucket) {
			return ContextFabricLedgerInvalidDebit, "bucket:" + string(debit.Bucket)
		}
		perCollection[debit.Collection]++
		perBucket.charge(debit.Bucket)
		byOrdinal, ok := ordinals[debit.Collection]
		if !ok {
			byOrdinal = map[int]struct{}{}
			ordinals[debit.Collection] = byOrdinal
		}
		byOrdinal[debit.Ordinal] = struct{}{}
	}

	// THE OCCURRENCE KEY: a collection charging n occurrences must charge
	// exactly 0..n-1.
	//
	// Density alone is the whole check, and a separate duplicate-ordinal
	// guard was removed as SUBSUMED rather than kept beside it: a repeated
	// ordinal necessarily leaves a gap in the range, because the range is
	// sized by the debit count. Two guards where one can fire is one guard
	// no fixture can isolate.
	// In VOCABULARY ORDER, not map order. Disagreement reaches telemetry, and
	// a result with two defective collections would otherwise name whichever
	// one Go's map iteration reached first -- different diagnosis, same
	// input, run to run.
	for _, collection := range contextFabricChargedCollections {
		byOrdinal := ordinals[collection]
		for ordinal := 0; ordinal < perCollection[collection]; ordinal++ {
			if _, present := byOrdinal[ordinal]; !present {
				return ContextFabricLedgerDuplicateOccurrence,
					string(collection) + "#" + strconv.Itoa(ordinal)
			}
		}
	}

	census := contextFabricCensusByCollection(ledger.Counts)
	for _, collection := range contextFabricChargedCollections {
		if perCollection[collection] != census[collection] {
			return ContextFabricLedgerCollectionDisagreement, string(collection)
		}
	}

	// In VOCABULARY ORDER, not map order: Disagreement reaches telemetry, and
	// a field whose value depends on Go's map iteration would name a
	// different bucket on each run for the same defect.
	for _, bucket := range contextFabricItemBuckets {
		if contextFabricAttributionBucket(perBucket, bucket) != contextFabricAttributionBucket(ledger.Attribution, bucket) {
			return ContextFabricLedgerBucketDisagreement, string(bucket)
		}
	}

	return ContextFabricLedgerReconciled, ""
}

// contextFabricRepeatedDeclaredIDs reports every id carried by more than one
// debit of the same collection, in vocabulary-then-id order.
//
// OBSERVATION ONLY -- it never decides the status. What the occurrence key
// cannot see is whether two positions hold the same SOURCE item: a producer
// that copies one item into two slots keeps the counts, the buckets, the total
// and the ordinal range intact. This is the signal for that, reported rather
// than enforced, because enforcing it once turned a real answer into a server
// error.
func contextFabricRepeatedDeclaredIDs(debits []ContextFabricItemDebit) []string {
	seen := map[ContextFabricChargedCollection]map[string]int{}
	for _, debit := range debits {
		if debit.DeclaredID == "" {
			continue
		}
		byID, ok := seen[debit.Collection]
		if !ok {
			byID = map[string]int{}
			seen[debit.Collection] = byID
		}
		byID[debit.DeclaredID]++
	}
	repeated := []string{}
	for _, collection := range contextFabricChargedCollections {
		byID, ok := seen[collection]
		if !ok {
			continue
		}
		ids := make([]string, 0, len(byID))
		for id, count := range byID {
			if count > 1 {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for _, id := range ids {
			repeated = append(repeated, string(collection)+":"+id)
		}
	}
	if len(repeated) == 0 {
		return nil
	}
	return repeated
}

// contextFabricAttributionBucket reads ONE bucket off an attribution by name.
//
// A switch over the closed vocabulary rather than reflection, so the mapping is
// a decision written down once. Its residual risk is the default arm: a fifth
// bucket would read zero on BOTH sides of the comparison above and the check
// would pass for exactly the bucket nobody had wired up yet. That is why
// TestEveryAttributionFieldIsReadableByItsBucket exists -- it walks the
// attribution struct by reflection and fails on a field no bucket can read.
func contextFabricAttributionBucket(attribution ContextFabricItemAttribution, bucket ContextFabricItemBucket) int {
	switch bucket {
	case ContextFabricItemBucketGlobal:
		return attribution.Global
	case ContextFabricItemBucketMember:
		return attribution.Member
	case ContextFabricItemBucketGroup:
		return attribution.Group
	case ContextFabricItemBucketMultiGroup:
		return attribution.MultiGroup
	default:
		return 0
	}
}

// A REPEATED DECLARED ID IS NOT AN ACCOUNTING DEFECT, and this file briefly
// treated it as one.
//
// The reasoning was that drivers, findings and claims carry ids the synthesis
// contract requires to be unique within an answer, so a repeat means two
// charged items look like one to any id-keyed consumer. The reasoning is sound
// and the consequence was wrong: an accounting defect is a SERVER error, and
// this seam's own rule is that a false one costs a served answer. Executing it
// proved the point immediately -- a real engine fixture assembles two claimed
// facts sharing `claim_workload_a`, so every answer of that shape became a 500
// on a 43-debit account that balanced perfectly.
//
// The account does not need it. What makes this ledger occurrence-level is the
// (Collection, Ordinal) key, checked above for uniqueness AND density: an
// occurrence dropped and another charged twice moves that key whatever the ids
// say. A repeated id is a PRODUCER defect for validation to reject, and it is
// carried on the debit as diagnosis so a reader can still see it.

// contextFabricCensusByCollection projects the per-collection census onto the
// charged-collection vocabulary.
//
// EVERY field of ContextFabricResultItemCounts is accounted for here or is
// declared excluded in contextFabricCensusExcludedFields, and
// TestEveryCensusFieldIsWalkedOrDeclaredExcluded walks the struct by reflection
// to prove it.
//
// WHAT THAT GUARD DOES AND DOES NOT COVER, stated precisely because an
// overstated guarantee is worse than a named gap. It catches a new census FIELD
// with no walker. It does NOT catch a new charged quantity FOLDED INTO an
// existing field -- adding len(result.Limitations) to counts.Candidates leaves
// the reflected shape unchanged. That case is not silent either, but it fails
// somewhere else and later: the walk would debit two candidates where the
// census counts two plus the limitations, and reconciliation reports a
// collection disagreement on `candidates` at runtime rather than a test failure
// at build time.
func contextFabricCensusByCollection(counts ContextFabricResultItemCounts) map[ContextFabricChargedCollection]int {
	return map[ContextFabricChargedCollection]int{
		ContextFabricChargedCandidates:    counts.Candidates,
		ContextFabricChargedCohortMembers: counts.CohortMembers,
		ContextFabricChargedDrivers:       counts.Drivers,
		ContextFabricChargedRemainingWork: counts.RemainingWork,
		ContextFabricChargedReadinessGaps: counts.ReadinessGaps,
		ContextFabricChargedConflicts:     counts.Conflicts,
		ContextFabricChargedClaimedFacts:  counts.ClaimedFacts,
	}
}

// contextFabricCensusExcludedFields names the census fields that are
// DELIBERATELY not charged, with the decision beside each. A field may be
// absent from the ledger only by appearing here.
var contextFabricCensusExcludedFields = map[string]string{
	// Graph-evidence relationship paths are excluded from the item budget
	// itself (CHAOS-4523, ContextFabricResultItemCounts.Budgeted), so they
	// mint no debit either. Charging them here would make the ledger and the
	// budget describe different answers.
	"Paths": "excluded from Budgeted() by CHAOS-4523",
}

// ContextFabricCapacityVerdict is the CLOSED vocabulary of what a capacity
// check found. It is a vocabulary rather than a bool because "did not fit",
// "was never bounded" and "could not be accounted for" are three different
// operator problems, and a bool reports all three as false.
type ContextFabricCapacityVerdict string

const (
	// ContextFabricCapacityCertifiedFit: the ledger reconciles and the
	// charged total is inside a positive MaxItems.
	ContextFabricCapacityCertifiedFit ContextFabricCapacityVerdict = "certified_fit"
	// ContextFabricCapacityOverdraw: the ledger reconciles and the answer
	// spent more than the ceiling. An ordinary budget overrun -- the
	// engine's retry, reduction and planned refusal apply.
	ContextFabricCapacityOverdraw ContextFabricCapacityVerdict = "overdraw"
	// ContextFabricCapacityUnbounded: no item ceiling is in force. NOT a
	// fit: an unbounded answer was never certified, and a caller that reads
	// "not overdrawn" as "certified" is the caller this member exists to
	// stop.
	ContextFabricCapacityUnbounded ContextFabricCapacityVerdict = "unbounded"
	// ContextFabricCapacityAccountingDefect: the ledger did not reconcile,
	// so no capacity statement can be made about it at all. A server defect,
	// never an over-budget answer -- see the engine's typed error.
	ContextFabricCapacityAccountingDefect ContextFabricCapacityVerdict = "accounting_defect"
)

var contextFabricCapacityVerdicts = [4]ContextFabricCapacityVerdict{
	ContextFabricCapacityCertifiedFit,
	ContextFabricCapacityOverdraw,
	ContextFabricCapacityUnbounded,
	ContextFabricCapacityAccountingDefect,
}

// ContextFabricCapacityVerdictCount is the closed vocabulary's size.
const ContextFabricCapacityVerdictCount = len(contextFabricCapacityVerdicts)

// ContextFabricCapacityVerdictVocabulary returns it in published order.
func ContextFabricCapacityVerdictVocabulary() [ContextFabricCapacityVerdictCount]ContextFabricCapacityVerdict {
	return contextFabricCapacityVerdicts
}

// ValidContextFabricCapacityVerdict reports membership; empty is not a member.
func ValidContextFabricCapacityVerdict(value ContextFabricCapacityVerdict) bool {
	for _, member := range contextFabricCapacityVerdicts {
		if member == value {
			return true
		}
	}
	return false
}

// ContextFabricCertifiedFit is proof that a RECONCILED ledger's charged total
// is inside a positive item ceiling.
//
// Its fields are unexported and it has exactly one constructor, which is the
// entire point of the type: the zero value certifies nothing, and no caller can
// assemble a certificate for an answer whose accounting did not reconcile. A
// boolean return would have been one `if` away from being ignored; a value that
// cannot be forged is not.
type ContextFabricCertifiedFit struct {
	certified bool
	items     int
	maxItems  int
}

// Certified reports whether this value is a real certificate.
func (c ContextFabricCertifiedFit) Certified() bool { return c.certified }

// Items is the charged total that was certified, zero on the zero value.
func (c ContextFabricCertifiedFit) Items() int { return c.items }

// MaxItems is the ceiling it was certified against, zero on the zero value.
func (c ContextFabricCertifiedFit) MaxItems() int { return c.maxItems }

// CertifyContextFabricCapacity is the ONLY way a Context Fabric result becomes
// a certified fit.
//
// A zero or negative MaxItems is UNBOUNDED and is not a fit: this function
// refuses to convert "nobody set a ceiling" into "we checked it against one".
// An unreconciled ledger is an accounting defect and gets no capacity statement
// at all -- measuring a document whose account does not add up would report a
// number about an answer nobody can describe.
func CertifyContextFabricCapacity(ledger ContextFabricItemLedger, budget ContextFabricResponseBudget) (ContextFabricCertifiedFit, ContextFabricCapacityVerdict) {
	// RE-DERIVED, never read off the field. Status is exported and the debit
	// slice is exported, so a caller can hold a genuinely reconciled ledger,
	// drop a debit, and leave the status saying `reconciled`. A certificate
	// that trusted that field would be exactly the forgeable thing this type
	// exists not to be, so the account is re-checked here against the debits
	// actually presented.
	//
	// The residual, stated rather than hidden: a caller that rewrites the
	// debits AND the census AND the split consistently has authored a
	// different ledger, and no check inside this package can tell that from a
	// real one. What is closed is the realistic shape -- debits edited in
	// place while the summaries stand.
	// PRODUCED BY THE RECONCILER, over a real result. A zero ledger is
	// perfectly self-consistent and bound to no answer, so the re-derivation
	// below would certify it for any positive ceiling; requiring provenance
	// is what makes the certificate a statement about a DOCUMENT rather than
	// about a struct.
	if !ledger.produced {
		return ContextFabricCertifiedFit{}, ContextFabricCapacityAccountingDefect
	}
	if status, _ := contextFabricReconcile(ledger); status != ContextFabricLedgerReconciled {
		return ContextFabricCertifiedFit{}, ContextFabricCapacityAccountingDefect
	}
	if budget.MaxItems <= 0 {
		return ContextFabricCertifiedFit{}, ContextFabricCapacityUnbounded
	}
	if ledger.Total() > budget.MaxItems {
		return ContextFabricCertifiedFit{}, ContextFabricCapacityOverdraw
	}
	return ContextFabricCertifiedFit{
		certified: true,
		items:     ledger.Total(),
		maxItems:  budget.MaxItems,
	}, ContextFabricCapacityCertifiedFit
}
