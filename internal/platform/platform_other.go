//go:build !windows

package platform

import "time"

func Autostart(string) AutostartState            { return AutostartOff }
func SetAutostart(bool, string, ...string) error { return ErrUnsupported }
func FirewallAllowed(string) bool                { return false }
func FirewallRuleExists(string) bool             { return false }
func RemoveFirewallFor(string)                   {}
func AllowFirewall(string) error                 { return ErrUnsupported }
func RemoveFirewall() error                      { return ErrUnsupported }
func IsAdmin() bool                              { return false }
func RunElevated(string, string) (int, error)    { return -1, ErrUnsupported }
func IdleTime() (time.Duration, error)           { return 0, ErrUnsupported }
