//go:build !cgo

package main

// The production plugin requires cgo and -buildmode=c-shared. This stub keeps
// portable configuration and RPC tests available on hosts without a C toolchain.
func main() {}
