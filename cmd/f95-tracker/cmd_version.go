package main

import (
	"context"
	"fmt"
)

func runVersion(ctx context.Context, args []string) error {
	fmt.Println(version)
	return nil
}
