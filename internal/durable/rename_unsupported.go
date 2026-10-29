//go:build !darwin || !cgo

package durable

// RenameExclusive rejects environments outside the verified persistence scope.
func RenameExclusive(_, _ string) error { return errUnsupported }
