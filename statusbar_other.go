//go:build !darwin

package main

func menubarAvailable() bool { return false }
func statusRun()             {}
