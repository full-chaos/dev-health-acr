package main

import "testing"

func TestParseDiagnosticsArgsRequiresExplicitOutput(t *testing.T) {
	if _, _, err := parseDiagnosticsArgs(nil); err == nil {
		t.Fatal("expected an error when --output is not provided")
	}
	if _, _, err := parseDiagnosticsArgs([]string{"--live"}); err == nil {
		t.Fatal("expected an error when --output is not provided alongside --live")
	}
}

func TestParseDiagnosticsArgsParsesOutputAndLive(t *testing.T) {
	output, live, err := parseDiagnosticsArgs([]string{"--output", "/tmp/bundle.tar", "--live"})
	if err != nil || output != "/tmp/bundle.tar" || !live {
		t.Fatalf("unexpected parse result: output=%q live=%v err=%v", output, live, err)
	}

	output, live, err = parseDiagnosticsArgs([]string{"--output=/tmp/other.tar"})
	if err != nil || output != "/tmp/other.tar" || live {
		t.Fatalf("unexpected parse result: output=%q live=%v err=%v", output, live, err)
	}
}

func TestParseDiagnosticsArgsRejectsDuplicateOutputAndLive(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "duplicate_output_same_form", args: []string{"--output", "/tmp/one.tar", "--output", "/tmp/two.tar"}},
		{name: "duplicate_output_mixed_form", args: []string{"--output=/tmp/one.tar", "--output", "/tmp/two.tar"}},
		{name: "duplicate_live", args: []string{"--output", "/tmp/one.tar", "--live", "--live"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := parseDiagnosticsArgs(tc.args); err == nil {
				t.Fatalf("parseDiagnosticsArgs(%v) unexpectedly succeeded", tc.args)
			}
		})
	}
}
