package dbacl

import (
	"os"
	"reflect"
	"regexp"
	"testing"
)

// The agent episode table is used only when episode writeback is on. With the
// flag on the runtime role holds exactly SELECT, INSERT and UPDATE on it; with
// it off it holds nothing, and the readiness check does not demand it. The
// expected set is written out here, not read from the declaration.
func TestAgentEpisodesGrantFollowsTheWritebackFlag(t *testing.T) {
	privileges := func(features Features) []string {
		var held []string
		for _, statement := range GrantStatements(`"role"`, features) {
			if statement.Table == "agent_episodes" {
				held = append(held, statement.Privileges...)
			}
		}
		return held
	}
	if got, want := privileges(Features{EpisodeWriteback: true}), []string{"SELECT", "INSERT", "UPDATE"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("writeback on: agent_episodes privileges = %v, want %v", got, want)
	}
	for name, features := range map[string]Features{"no features": {}, "only workload exchange": {WorkloadTokenExchange: true}} {
		if got := privileges(features); len(got) != 0 {
			t.Fatalf("%s: agent_episodes privileges = %v, want none", name, got)
		}
	}

	source, err := os.ReadFile("../runtime/hosted/postgres_readiness.go")
	if err != nil {
		t.Fatal(err)
	}
	gated := regexp.MustCompile(`NOT \$1\s+OR \(has_table_privilege\(current_user, 'acr\.agent_episodes', 'SELECT'\)\s+AND has_table_privilege\(current_user, 'acr\.agent_episodes', 'INSERT'\)\)`)
	if !gated.Match(source) {
		t.Fatal("the readiness check must demand agent_episodes only when episode writeback is on")
	}
	if n := len(regexp.MustCompile(`agent_episodes`).FindAll(source, -1)); n != 2 {
		t.Fatalf("agent_episodes appears %d times in the readiness check, want only the two gated assertions", n)
	}
}
