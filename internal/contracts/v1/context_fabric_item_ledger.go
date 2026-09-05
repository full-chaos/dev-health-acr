package v1

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
	// finding_id, claim_id -- or, for a collection whose items carry no id,
	// the subject key. It is diagnosis, and it is what a duplicate check can
	// key on; it is never the occurrence key, because a producer that
	// repeats an id must still be counted twice here rather than collapsing
	// two items into one.
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
	// ContextFabricLedgerDuplicateOccurrence: two occurrences in one
	// collection declare the same id, so any consumer keying on that id sees
	// one item where the budget charges two.
	ContextFabricLedgerDuplicateOccurrence ContextFabricLedgerStatus = "duplicate_occurrence"
	// ContextFabricLedgerTotalDisagreement: the debit count and Budgeted()
	// differ. Kept as its own member rather than folded into the collection
	// case so a total-level break is never reported as a per-collection one.
	ContextFabricLedgerTotalDisagreement ContextFabricLedgerStatus = "total_disagreement"
)

var contextFabricLedgerStatuses = [5]ContextFabricLedgerStatus{
	ContextFabricLedgerReconciled,
	ContextFabricLedgerCollectionDisagreement,
	ContextFabricLedgerBucketDisagreement,
	ContextFabricLedgerDuplicateOccurrence,
	ContextFabricLedgerTotalDisagreement,
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
	// Attribution is the INDEPENDENT bucket split, from the shipped
	// function. Two independent derivations are the point: a ledger that
	// only agreed with itself would certify its own mistakes.
	Attribution ContextFabricItemAttribution `json:"attribution"`
	// Status is what reconciliation found.
	Status ContextFabricLedgerStatus `json:"status"`
	// Disagreement NAMES the collection or bucket that disagreed, empty when
	// reconciled. A status without a name sends an operator back to the
	// source; this is the field that makes the defect diagnosable from the
	// artifact.
	Disagreement string `json:"disagreement,omitempty"`

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
			DeclaredID: contextFabricSubjectBucketKey(candidate.Subject),
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

// contextFabricReconcile is the accounting check: three independent derivations
// of the same answer, compared.
//
// ORDER MATTERS and is stated rather than incidental: a duplicate id is
// reported before a count disagreement, because a duplicate is a statement
// about WHICH items exist and a count is a statement about how many, and the
// first explains the second wherever both are true.
func contextFabricReconcile(ledger ContextFabricItemLedger) (ContextFabricLedgerStatus, string) {
	seen := map[ContextFabricChargedCollection]map[string]struct{}{}
	perCollection := map[ContextFabricChargedCollection]int{}
	perBucket := ContextFabricItemAttribution{}
	for _, debit := range ledger.Debits {
		perCollection[debit.Collection]++
		perBucket.charge(debit.Bucket)
		if !contextFabricCollectionHasUniqueIDs(debit.Collection) || debit.DeclaredID == "" {
			continue
		}
		ids, ok := seen[debit.Collection]
		if !ok {
			ids = map[string]struct{}{}
			seen[debit.Collection] = ids
		}
		if _, duplicate := ids[debit.DeclaredID]; duplicate {
			return ContextFabricLedgerDuplicateOccurrence,
				string(debit.Collection) + ":" + debit.DeclaredID
		}
		ids[debit.DeclaredID] = struct{}{}
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

	if ledger.Total() != ledger.Counts.Budgeted() {
		return ContextFabricLedgerTotalDisagreement, ""
	}
	return ContextFabricLedgerReconciled, ""
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

// contextFabricCollectionHasUniqueIDs reports whether a collection's items
// declare an id the answer contract requires to be unique.
//
// An ALLOW-LIST, so a new collection is not silently enrolled in a duplicate
// check its items were never promised to satisfy. Drivers, findings and claims
// carry ids the synthesis contract requires to be unique within an answer;
// cohort member rows are keyed by subject and a cohort carrying one member
// twice is a real defect. Resolution candidates are NOT checked: nothing
// promises the resolver returns each subject once, and a false accounting
// defect is worse than an unchecked duplicate, because it turns a servable
// answer into a server error.
func contextFabricCollectionHasUniqueIDs(collection ContextFabricChargedCollection) bool {
	switch collection {
	case ContextFabricChargedDrivers,
		ContextFabricChargedRemainingWork,
		ContextFabricChargedReadinessGaps,
		ContextFabricChargedConflicts,
		ContextFabricChargedClaimedFacts,
		ContextFabricChargedCohortMembers:
		return true
	case ContextFabricChargedCandidates:
		return false
	default:
		return false
	}
}

// contextFabricCensusByCollection projects the per-collection census onto the
// charged-collection vocabulary.
//
// EVERY field of ContextFabricResultItemCounts is accounted for here or is
// declared excluded in contextFabricCensusExcludedFields, and
// TestEveryCensusFieldIsWalkedOrDeclaredExcluded walks the struct by reflection
// to prove it. That test is the reason a future collection cannot be added to
// the budget without a walker in this file: the census would charge it and the
// ledger would not, which is precisely the class this ledger exists to make
// impossible.
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
	if !ledger.Reconciled() {
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
