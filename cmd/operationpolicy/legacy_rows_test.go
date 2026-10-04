package main

import (
	"slices"
	"strings"
	"testing"
)

func firstLegacy(t *testing.T, in inputs) registryRow {
	t.Helper()
	for _, row := range in.Registry.Rows {
		if row.Legacy {
			return row
		}
	}
	t.Fatal("vendored registry has no legacy row")
	return registryRow{}
}

func TestLegacyRowsDoNotChangeTheClassification(t *testing.T) {
	in := vendoredInputs(t)
	var current, legacy int
	for _, row := range in.Registry.Rows {
		if row.Legacy {
			legacy++
		} else {
			current++
		}
	}
	if legacy == 0 {
		t.Fatal("vendored registry has no legacy row")
	}
	file, err := build(in)
	if err != nil {
		t.Fatal(err)
	}
	if file.Source.RegistryRows != current {
		t.Fatalf("registry_rows = %d, want the %d current rows", file.Source.RegistryRows, current)
	}
	if got := len(file.Operations) + len(file.NotServed); got != current {
		t.Fatalf("%d operations classified, want %d", got, current)
	}
	for _, op := range file.Operations {
		for _, row := range in.Registry.Rows {
			if row.Legacy && row.Operation == op.Name && row.Digest == op.Digest {
				t.Fatalf("%s is pinned on its legacy digest", op.Name)
			}
		}
	}
}

func TestLegacyRowIsCheckedLikeAnyRow(t *testing.T) {
	cases := map[string]func(*inputs, int){
		"digest does not recompute": func(in *inputs, i int) { in.Registry.Rows[i].Digest = strings.Repeat("0", 64) },
		"no current row":            func(in *inputs, i int) { in.Registry.Rows[i].Operation = "noSuchOperation" },
		"digest of the current row": func(in *inputs, i int) {
			cur := in.Registry.Rows[rowIndex(t, *in, in.Registry.Rows[i].Operation)]
			in.Registry.Rows[i].Document, in.Registry.Rows[i].Digest = cur.Document, cur.Digest
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := cloneInputs(vendoredInputs(t))
			i := slices.IndexFunc(in.Registry.Rows, func(r registryRow) bool { return r.Legacy })
			mutate(&in, i)
			if _, err := build(in); err == nil {
				t.Fatal("generation accepted a bad legacy row")
			}
		})
	}
}

func TestSecondCurrentRowOfAnOperationStillFails(t *testing.T) {
	in := cloneInputs(vendoredInputs(t))
	firstLegacy(t, in)
	i := slices.IndexFunc(in.Registry.Rows, func(r registryRow) bool { return r.Legacy })
	in.Registry.Rows[i].Legacy = false
	if _, err := build(in); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("want the twice refusal, got %v", err)
	}
}

func TestLegacyTextIsNeverOfferedToClients(t *testing.T) {
	in := vendoredInputs(t)
	artifact, err := generate(in)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, row := range in.Registry.Rows {
		if !row.Legacy {
			continue
		}
		seen++
		if strings.Contains(string(artifact), row.Digest) {
			t.Errorf("%s: legacy digest %s is in the artifact", row.Operation, row.Digest)
		}
		if strings.Contains(string(artifact), strings.ReplaceAll(row.Document, "\n", `\n`)) {
			t.Errorf("%s: legacy document text is in the artifact", row.Operation)
		}
	}
	if seen == 0 {
		t.Fatal("vendored registry has no legacy row")
	}
}
