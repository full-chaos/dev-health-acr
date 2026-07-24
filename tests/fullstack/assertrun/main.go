// Command assertrun is the CHAOS-3065 full-stack acceptance gate's assertion tool
// (docs/fullstack-acceptance.md sections 3 and 7). It never starts a server or touches
// Docker: it reads fixture and artifact files from disk and shells out only via the
// caller-provided --probe-command. It has two subcommands:
//
//	verify-fixture     re-derives corpus/seed hashes and row-count/scope probes before any
//	                   client runs (docs section 4.3 preflight).
//	verify-seed-schema offline check that every seed INSERT INTO's table/columns exist in the
//	                   effective ClickHouse schema replayed from the ops migration directory,
//	                   with matching VALUES tuple arity. Needs neither Docker nor ClickHouse.
//	assert-run         validates one task's captured artifacts against its oracle across the
//	                   layered checks in docs section 7, emitting assertion-report.json and
//	                   junit.xml.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: assertrun <verify-fixture|assert-run> [flags]")
		os.Exit(2)
	}
	var code int
	switch os.Args[1] {
	case "verify-fixture":
		code = runVerifyFixture(os.Args[2:])
	case "verify-seed-schema":
		code = runVerifySeedSchema(os.Args[2:])
	case "assert-run":
		code = runAssertRun(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "assertrun: unknown subcommand %q\n", os.Args[1])
		code = 2
	}
	os.Exit(code)
}
