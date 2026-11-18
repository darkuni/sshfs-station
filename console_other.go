//go:build !windows

package main

func setupConsole() bool { return true }

func messageBox(title, msg string, isError bool) {}
