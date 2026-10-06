package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

func main() {
	root := flag.String("root", ".", "repository root or a path inside it")
	write := flag.Bool("write", false, "regenerate derived contract artifacts")
	quiet := flag.Bool("quiet", false, "suppress successful checks")
	requiredBaseline := flag.Bool("required-baseline", false, "fail when a published schema newly requires a field the previous release tag did not")
	flag.Parse()

	if *requiredBaseline {
		if err := contractcheck.CheckRequiredAgainstTag(contractcheck.RequiredBaselineOptions{Root: *root, Out: os.Stdout}); err != nil {
			fmt.Fprintf(os.Stderr, "contractcheck: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := contractcheck.Run(contractcheck.Options{
		Root:  *root,
		Write: *write,
		Quiet: *quiet,
		Out:   os.Stdout,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "contractcheck: %v\n", err)
		os.Exit(1)
	}
}
