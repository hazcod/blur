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
	fmt.Println("No CgoFiles or import C found in non-standard repository dependencies across supported targets")
}
