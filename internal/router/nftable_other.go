//go:build !linux

package router

func nftTableGone(string) bool { return false }
