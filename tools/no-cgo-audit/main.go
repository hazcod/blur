package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	if err := AuditSupportedTargets(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("No import C found in Blur source or cgo-disabled dependency graphs across supported targets")
}
