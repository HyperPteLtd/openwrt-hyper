package netcli

import (
	"bufio"
	"context"
	"fmt"
	"github.com/HyperPteLtd/openwrt-hyper/internal/cnlist"
	"github.com/HyperPteLtd/openwrt-hyper/internal/netguard"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
	"os"
	"os/signal"
	"syscall"
)

// cnRanges prints the mainland China ranges of one family as nft set elements.
func cnRanges(v6 bool) error {
	data, err := os.ReadFile(cnlist.Path(cnlist.Addresses))
	if err != nil {
		return err
	}
	ranges, err := subscription.MRSRanges(data)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(os.Stdout)
	for _, r := range ranges {
		if r[0].Is6() == v6 {
			if r[0] == r[1] {
				fmt.Fprintln(w, r[0])
			} else {
				fmt.Fprintf(w, "%s-%s\n", r[0], r[1])
			}
		}
	}
	return w.Flush()
}

func Main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var e error
	if len(os.Args) == 2 && (os.Args[1] == "management-record" || os.Args[1] == "management-check") {
		e = netguard.Management(ctx, os.Args[1] == "management-record")
	} else if len(os.Args) == 3 && os.Args[1] == "cn-ranges" && (os.Args[2] == "4" || os.Args[2] == "6") {
		e = cnRanges(os.Args[2] == "6")
	} else if len(os.Args) >= 2 && os.Args[1] == "dns-directories" {
		e = netguard.DNSDirectories(os.Args[2:], os.Stdout)
	} else {
		e = netguard.Run(ctx, os.Args[1:], os.Stdout)
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
