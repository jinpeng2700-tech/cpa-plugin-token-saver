//go:build !linux || !cgo

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "the dynamic ABI host requires Linux with cgo")
	os.Exit(2)
}
