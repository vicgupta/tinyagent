package main

import (
	"flag"
	"os"
	"strings"
	"testing"
)

// TestCLIReferenceListsEveryFlag fails when a registered flag is missing
// from the flag table in docs/subcommands.md.
func TestCLIReferenceListsEveryFlag(t *testing.T) {
	b, err := os.ReadFile("../../docs/subcommands.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	newFlagSet(&options{}).VisitAll(func(f *flag.Flag) {
		dash := "--"
		if len(f.Name) == 1 {
			dash = "-"
		}
		name := dash + f.Name
		for _, prefix := range []string{"`", ", ", " "} {
			for _, end := range []string{"`", " ", "=", "[", ","} {
				if strings.Contains(doc, prefix+name+end) {
					return
				}
			}
		}
		t.Errorf("flag %s is not documented in docs/subcommands.md", name)
	})
}
