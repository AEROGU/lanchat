//go:build !windows

package ui

import "log/slog"

// notifier no muestra nada fuera de Windows (la página sigue avisando).
type notifier struct{}

func newNotifier(string, func(), *slog.Logger) *notifier { return &notifier{} }

func (*notifier) notify(string, string) {}

// UnregisterNotifications no hace nada fuera de Windows.
func UnregisterNotifications() error { return nil }
