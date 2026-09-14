package hosted_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestFileExchangeRejectsExplicitNullMemberQualifier(t *testing.T) {
	for _, form := range []string{"outer", "scoped_operand"} {
		form := form
		t.Run(form, func(t *testing.T) {
			dir := t.TempDir()
			runtime, err := newFileExchangeRuntime(dir, "test-model", 10*time.Second)
			if err != nil {
				t.Fatalf("newFileExchangeRuntime() error = %v", err)
			}
			runtime.poll = 5 * time.Millisecond

			done := make(chan struct{})
			go func() {
				defer close(done)
				respondToOneExchangeRequest(t, dir, fileExchangeOutputWithNullQualifier(form))
			}()

			_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validFileExchangeRequest())
			<-done
			if err == nil || !errors.Is(err, contextfabric.ErrModelOutput) {
				t.Fatalf("InterpretQuestion() error = %v, want explicit null to be rejected as model output", err)
			}
			if receipt.Outcome != "invalid_output" {
				t.Fatalf("receipt.Outcome = %q, want invalid_output", receipt.Outcome)
			}
		})
	}
}

func fileExchangeOutputWithNullQualifier(form string) string {
	field := `"member_qualifier":null`
	var expression string
	if form == "outer" {
		expression = `{"kind":"children_of_scope","anchor_terms":["Project Alpha"],"member_kind":"work_item",` + field + `}`
	} else {
		scoped := `{"kind":"children_of_scope","anchor_terms":["Project Alpha"],"member_kind":"work_item",` + field + `}`
		expression = `{"kind":"explicit_set","operands":[{"kind":"named_subject","terms":["Project Alpha"]},` + scoped + `]}`
	}
	return `{"shape":"open","requested_judgment":"status","subject_terms":["Project Alpha"],"time_context":{"axis":"current"},"fact_requirements":[{"kind":"status"}],"clarification_needed":false,"question_frame":{"goals":["assess_state"],"subject_expression":` + expression + `,"temporal":"current"}}`
}
