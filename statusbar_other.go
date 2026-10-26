//go:build !darwin && !windows

package main

func menubarAvailable() bool { return false }
func statusRun()             {}

func beforeExit() {}

func openURL(string) {}
func trayLogInit()   {}
