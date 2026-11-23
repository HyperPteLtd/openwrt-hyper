//go:build !linux

package subscription

import (
	"context"
	"errors"
	"net"
)

func dialAsGroup(context.Context, int, string, string) (net.Conn, error) {
	return nil, errors.New("socket_group_unavailable")
}
