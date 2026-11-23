//go:build !linux

package kernelcli

import "errors"

func execRestricted([]string) error { return errors.New("restricted core execution requires Linux") }
