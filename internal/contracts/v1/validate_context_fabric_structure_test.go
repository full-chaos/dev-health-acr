package v1

import (
	"encoding/json"
	"testing"
)

func TestContextFabricKindOption_Validate(t *testing.T) {
	t.Parallel()
	base := ContextFabricKindOption{
		ReceiptID: "kindr_confirm00001", OptionID: "opt_pr", Label: "a pull request",
		Kind: ContextFabricSubjectPullRequest, OfferSource: ContextFabricStructureOfferEngine,
	}
	cases := []struct {
		name    string
		mutate  func(*ContextFabricKindOption)
		wantErr bool
	}{
		{"well formed", func(o *ContextFabricKindOption) {}, false},
		{"missing namespace prefix", func(o *ContextFabricKindOption) { o.ReceiptID = "subr_confirm00001" }, true},
		{"invalid kind", func(o *ContextFabricKindOption) { o.Kind = "bogus" }, true},
		{"invalid offer_source", func(o *ContextFabricKindOption) { o.OfferSource = "bogus" }, true},
		{"empty label", func(o *ContextFabricKindOption) { o.Label = "" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opt := base
			tc.mutate(&opt)
			if err := opt.Validate(); (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestContextFabricAnchorOption_Validate(t *testing.T) {
	t.Parallel()
	base := ContextFabricAnchorOption{
		ReceiptID: "ancr_confirm00001", OptionID: "opt_repo", Label: "the ask-dev repository",
		Kind: ContextFabricSubjectRepository, CanonicalID: "repository_ask_dev", ClaimantKey: "claimant_key_opaque_1",
		OfferSource: ContextFabricStructureOfferEngine,
	}
	cases := []struct {
		name    string
		mutate  func(*ContextFabricAnchorOption)
		wantErr bool
	}{
		{"well formed", func(o *ContextFabricAnchorOption) {}, false},
		{"missing namespace prefix", func(o *ContextFabricAnchorOption) { o.ReceiptID = "kindr_confirm00001" }, true},
		{"empty claimant_key", func(o *ContextFabricAnchorOption) { o.ClaimantKey = "" }, true},
		{"empty canonical_id", func(o *ContextFabricAnchorOption) { o.CanonicalID = "" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opt := base
			tc.mutate(&opt)
			if err := opt.Validate(); (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestContextFabricHandleOption_Validate(t *testing.T) {
	t.Parallel()
	base := ContextFabricHandleOption{
		ReceiptID: "handr_confirm00001", OptionID: "opt_pr123", Label: "PR #123",
		Kind: ContextFabricSubjectPullRequest, PatternID: "pr_number", Value: "123", SourceColumn: "pull_requests.number",
		OfferSource: ContextFabricStructureOfferEngine,
	}
	cases := []struct {
		name    string
		mutate  func(*ContextFabricHandleOption)
		wantErr bool
	}{
		{"well formed", func(o *ContextFabricHandleOption) {}, false},
		{"missing namespace prefix", func(o *ContextFabricHandleOption) { o.ReceiptID = "ancr_confirm00001" }, true},
		{"empty pattern_id", func(o *ContextFabricHandleOption) { o.PatternID = "" }, true},
		{"empty source_column", func(o *ContextFabricHandleOption) { o.SourceColumn = "" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opt := base
			tc.mutate(&opt)
			if err := opt.Validate(); (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestContextFabricStructureNeeds_RoundTrip is the contract round-trip test
// (P1 acceptance criterion, mirroring W1's WindowClarification round-trip):
// a validly-constructed StructureNeeds survives a JSON marshal/unmarshal
// cycle byte-for-byte in shape and re-validates.
func TestContextFabricStructureNeeds_RoundTrip(t *testing.T) {
	t.Parallel()
	original := ContextFabricStructureNeeds{
		Missing: []ContextFabricStructureNeedKind{ContextFabricStructureNeedExpectedKind, ContextFabricStructureNeedSubjectAnchor},
		KindOptions: []ContextFabricKindOption{
			{ReceiptID: "kindr_confirm00001", OptionID: "opt_pr", Label: "a pull request", Kind: ContextFabricSubjectPullRequest, OfferSource: ContextFabricStructureOfferEngine},
		},
		AnchorOptions: []ContextFabricAnchorOption{
			{ReceiptID: "ancr_confirm00001", OptionID: "opt_repo", Label: "the ask-dev repository", Kind: ContextFabricSubjectRepository, CanonicalID: "repository_ask_dev", ClaimantKey: "claimant_key_opaque_1", OfferSource: ContextFabricStructureOfferEngine},
		},
		HandleOptions: []ContextFabricHandleOption{
			{ReceiptID: "handr_confirm00001", OptionID: "opt_pr123", Label: "PR #123", Kind: ContextFabricSubjectPullRequest, PatternID: "pr_number", Value: "123", SourceColumn: "pull_requests.number", OfferSource: ContextFabricStructureOfferEngine},
		},
		AcceptedGrammars: []ContextFabricAcceptedGrammar{
			{Member: ContextFabricStructureNeedSubjectHandle, Kind: ContextFabricSubjectPullRequest, PatternID: "pr_number"},
		},
	}
	if err := original.Validate(); err != nil {
		t.Fatalf("Validate() on the original error = %v", err)
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var decoded ContextFabricStructureNeeds
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("Validate() on the round-tripped value error = %v", err)
	}
	reencoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("re-Marshal() error = %v", err)
	}
	if string(encoded) != string(reencoded) {
		t.Errorf("round trip is not byte-identical:\n original = %s\n reencoded = %s", encoded, reencoded)
	}
}

func TestContextFabricStructureNeeds_Validate(t *testing.T) {
	t.Parallel()
	validKind := ContextFabricKindOption{ReceiptID: "kindr_confirm00001", OptionID: "opt_a", Label: "a", Kind: ContextFabricSubjectPullRequest, OfferSource: ContextFabricStructureOfferEngine}
	validKind2 := ContextFabricKindOption{ReceiptID: "kindr_confirm00002", OptionID: "opt_b", Label: "b", Kind: ContextFabricSubjectWorkItem, OfferSource: ContextFabricStructureOfferEngine}

	t.Run("empty missing is rejected", func(t *testing.T) {
		n := ContextFabricStructureNeeds{}
		if err := n.Validate(); err == nil {
			t.Fatal("Validate() = nil, want an error: missing must be non-empty")
		}
	})
	t.Run("duplicate missing entry is rejected", func(t *testing.T) {
		n := ContextFabricStructureNeeds{Missing: []ContextFabricStructureNeedKind{ContextFabricStructureNeedExpectedKind, ContextFabricStructureNeedExpectedKind}}
		if err := n.Validate(); err == nil {
			t.Fatal("Validate() = nil, want an error: missing entries must be unique")
		}
	})
	t.Run("duplicate receipt_id across DIFFERENT offer lists is rejected", func(t *testing.T) {
		n := ContextFabricStructureNeeds{
			Missing:     []ContextFabricStructureNeedKind{ContextFabricStructureNeedExpectedKind},
			KindOptions: []ContextFabricKindOption{validKind},
			AnchorOptions: []ContextFabricAnchorOption{
				{ReceiptID: validKind.ReceiptID, OptionID: "opt_other", Label: "x", Kind: ContextFabricSubjectRepository, CanonicalID: "c", ClaimantKey: "k", OfferSource: ContextFabricStructureOfferEngine},
			},
		}
		if err := n.Validate(); err == nil {
			t.Fatal("Validate() = nil, want an error: receipt_id must be unique across every offer list, not just within one")
		}
	})
	t.Run("well formed with two kind options is accepted", func(t *testing.T) {
		n := ContextFabricStructureNeeds{Missing: []ContextFabricStructureNeedKind{ContextFabricStructureNeedExpectedKind}, KindOptions: []ContextFabricKindOption{validKind, validKind2}}
		if err := n.Validate(); err != nil {
			t.Errorf("Validate() error = %v, want nil", err)
		}
	})
}

// TestContextFabricConfirmedStructureEntry_Validate pins the source/receipt-
// identity coupling: a receipt-sourced entry must carry the receipt it
// redeemed, and every other source must not (design brief section 2.1's
// per-carried-member echo -- a receipt identity on a non-receipt entry
// would misrepresent how the value actually entered).
func TestContextFabricConfirmedStructureEntry_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		entry   ContextFabricConfirmedStructureEntry
		wantErr bool
	}{
		{
			name: "receipt source with receipt identity is accepted",
			entry: ContextFabricConfirmedStructureEntry{
				Member: ContextFabricStructureNeedExpectedKind, AppliedValue: "pull_request", Source: ContextFabricStructureSourceReceipt,
				PriorResultID: "result_12345678", ReceiptID: "kindr_confirm00001", Provenance: ContextFabricStructureClarificationConfirmed, Disposition: ContextFabricStructureDispositionApplied,
			},
			wantErr: false,
		},
		{
			name: "receipt source WITHOUT receipt identity is rejected",
			entry: ContextFabricConfirmedStructureEntry{
				Member: ContextFabricStructureNeedExpectedKind, AppliedValue: "pull_request", Source: ContextFabricStructureSourceReceipt,
				Provenance: ContextFabricStructureClarificationConfirmed, Disposition: ContextFabricStructureDispositionApplied,
			},
			wantErr: true,
		},
		{
			name: "explicit source WITH a receipt identity is rejected",
			entry: ContextFabricConfirmedStructureEntry{
				Member: ContextFabricStructureNeedExpectedKind, AppliedValue: "pull_request", Source: ContextFabricStructureSourceExplicit,
				PriorResultID: "result_12345678", ReceiptID: "kindr_confirm00001", Provenance: ContextFabricStructureQuestionStated, Disposition: ContextFabricStructureDispositionApplied,
			},
			wantErr: true,
		},
		{
			name: "explicit_unattributed source with no receipt identity is accepted",
			entry: ContextFabricConfirmedStructureEntry{
				Member: ContextFabricStructureNeedWindow, AppliedValue: "trailing_90d", Source: ContextFabricStructureSourceExplicitUnattributed,
				Provenance: ContextFabricStructureInferredDefault, Disposition: ContextFabricStructureDispositionApplied,
			},
			wantErr: false,
		},
		{
			name: "vetoed disposition with no receipt identity is accepted (a veto the caller cannot see is the silent drop reborn)",
			entry: ContextFabricConfirmedStructureEntry{
				Member: ContextFabricStructureNeedSubjectAnchor, AppliedValue: "n/a", Source: ContextFabricStructureSourceExplicit,
				Provenance: ContextFabricStructureQuestionStated, Disposition: ContextFabricStructureDispositionVetoedConflict,
			},
			wantErr: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.entry.Validate(); (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestContextFabricInvestigationRequest_StructureReceiptNamespaces pins the
// closed structure-receipt-namespace set (design brief section 2.5 row 1):
// each of prior_kind_receipts/prior_anchor_receipts/prior_handle_receipts
// requires its OWN prefix and rejects every other structure namespace
// (including window's winr_ and each other's), and none of the four may
// appear in prior_subject_receipts.
func TestContextFabricInvestigationRequest_StructureReceiptNamespaces(t *testing.T) {
	t.Parallel()
	receipt := func(id string) ContextFabricBoundSubjectReceipt {
		return ContextFabricBoundSubjectReceipt{ResultID: "result_12345678", ReceiptID: id}
	}
	cases := []struct {
		name    string
		mutate  func(*ContextFabricInvestigationRequest)
		wantErr bool
	}{
		{"well formed kind receipt", func(r *ContextFabricInvestigationRequest) {
			r.PriorKindReceipts = []ContextFabricBoundSubjectReceipt{receipt("kindr_confirm00001")}
		}, false},
		{"well formed anchor receipt", func(r *ContextFabricInvestigationRequest) {
			r.PriorAnchorReceipts = []ContextFabricBoundSubjectReceipt{receipt("ancr_confirm00001")}
		}, false},
		{"well formed handle receipt", func(r *ContextFabricInvestigationRequest) {
			r.PriorHandleReceipts = []ContextFabricBoundSubjectReceipt{receipt("handr_confirm00001")}
		}, false},
		{"kindr_ id in prior_anchor_receipts is rejected", func(r *ContextFabricInvestigationRequest) {
			r.PriorAnchorReceipts = []ContextFabricBoundSubjectReceipt{receipt("kindr_confirm00001")}
		}, true},
		{"ancr_ id in prior_handle_receipts is rejected", func(r *ContextFabricInvestigationRequest) {
			r.PriorHandleReceipts = []ContextFabricBoundSubjectReceipt{receipt("ancr_confirm00001")}
		}, true},
		{"winr_ id in prior_kind_receipts is rejected", func(r *ContextFabricInvestigationRequest) {
			r.PriorKindReceipts = []ContextFabricBoundSubjectReceipt{receipt("winr_confirm00001")}
		}, true},
		{"kindr_ id in prior_subject_receipts is rejected", func(r *ContextFabricInvestigationRequest) {
			r.PriorSubjectReceipts = []ContextFabricBoundSubjectReceipt{receipt("kindr_confirm00001")}
		}, true},
		{"ancr_ id in prior_subject_receipts is rejected", func(r *ContextFabricInvestigationRequest) {
			r.PriorSubjectReceipts = []ContextFabricBoundSubjectReceipt{receipt("ancr_confirm00001")}
		}, true},
		{"handr_ id in prior_subject_receipts is rejected", func(r *ContextFabricInvestigationRequest) {
			r.PriorSubjectReceipts = []ContextFabricBoundSubjectReceipt{receipt("handr_confirm00001")}
		}, true},
		{"duplicate kind receipt is rejected", func(r *ContextFabricInvestigationRequest) {
			r.PriorKindReceipts = []ContextFabricBoundSubjectReceipt{receipt("kindr_confirm00001"), receipt("kindr_confirm00001")}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := validContextFabricContractRequest()
			tc.mutate(&req)
			if err := req.Validate(); (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestContextFabricStructureOfferSnapshotEntry_Validate(t *testing.T) {
	t.Parallel()
	base := ContextFabricStructureOfferSnapshotEntry{
		Member: ContextFabricStructureNeedExpectedKind, OfferID: "opt_a", Rank: 0, OfferSource: ContextFabricStructureOfferEngine,
	}
	if err := base.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
	negative := base
	negative.Rank = -1
	if err := negative.Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error: rank must be non-negative")
	}
}
