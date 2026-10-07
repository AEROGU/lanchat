//go:build !windows

package platform

func AutostartEnabled() bool                     { return false }
func SetAutostart(bool, string, ...string) error { return ErrUnsupported }
func FirewallAllowed(string) bool                { return false }
func AllowFirewall(string) error                 { return ErrUnsupported }
func RemoveFirewall() error                      { return ErrUnsupported }
func IsAdmin() bool                              { return false }
func RunElevated(string, string) (int, error)    { return -1, ErrUnsupported }
