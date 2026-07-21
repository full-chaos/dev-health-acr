package main

import (
	"fmt"
	"os"

	"github.com/full-chaos/dev-health-acr/internal/nativeadapters"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	client := nativeadapters.Client(os.Args[1])
	if err := nativeadapters.Record(client, os.Args[2:], os.Environ(), mustGetwd(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func mustGetwd() string { directory, _ := os.Getwd(); return directory }
