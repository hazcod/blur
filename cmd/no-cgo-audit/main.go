package main

import (
	"context"
	"fmt"
	"os"

	"github.com/nielsgroen/blur/internal/nocgoaudit"
)

func main() {
	if err := nocgoaudit.AuditSupportedTargets(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("No CgoFiles or target-selected import C found in Blur source or non-standard dependencies across supported targets")
}
