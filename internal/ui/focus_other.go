//go:build !windows

package ui

// focusWindow no está implementado fuera de Windows.
func focusWindow() bool { return false }
