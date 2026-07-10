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
	flag.Parse()

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
