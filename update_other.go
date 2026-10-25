//go:build !darwin && !windows

package main

import "errors"

func updateKey() string        { return "" }
func updateSupported() bool    { return false }
func applyUpdate(string) error { return errors.New("not supported") }
func updateCleanup()           {}
func waitOldInstance()         {}
