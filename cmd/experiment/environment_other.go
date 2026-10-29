//go:build !darwin

package main

import "fmt"

type storageInfo struct{}

func readStorage(string) (storageInfo, error) {
	return storageInfo{}, fmt.Errorf("experiment storage environment unsupported")
}
