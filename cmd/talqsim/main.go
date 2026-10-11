// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Command talqsim serves a simulated TALQ 2.x gateway with two streetlight
// controllers, so a TALQ CMS can be tried without a gateway. For example:
//
//	talqsim -listen 127.0.0.1:8095 -token demo-gw-token
//	talqsim -cms http://127.0.0.1:8096/talq -cms-token demo-cms-token
//
// The gateway API is at /talq. With -cms the gateway also announces its
// services and devices to that CMS API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cB-Sigma/talq"
	"github.com/cB-Sigma/talq-sim/sim"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, nil); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "talqsim:", err)
		os.Exit(1)
	}
}

// run serves until ctx ends; ready, when not nil, receives the gateway.
func run(ctx context.Context, args []string, out io.Writer, ready chan<- *sim.Gateway) error {
	fs := flag.NewFlagSet("talqsim", flag.ContinueOnError)
	fs.SetOutput(out)
	listen := fs.String("listen", "127.0.0.1:8095", "address to listen on")
	address := fs.String("address", "6f1c0000-0000-4000-8000-00000000d001", "the gateway's TALQ address (a UUID)")
	token := fs.String("token", "", "bearer token the CMS must send (empty: none)")
	cms := fs.String("cms", "", "CMS API base to announce to (empty: none)")
	cmsToken := fs.String("cms-token", "", "bearer token sent to the CMS")
	walk := fs.Bool("walk", false, "run every gateway operation against the gateway once at start (a self-test)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	g, err := sim.NewAt(*listen, *address, *token)
	if err != nil {
		return err
	}
	defer g.Close()
	g.CMSToken = *cmsToken
	fmt.Fprintf(out, "talqsim gateway %s at %s\n", g.Address, g.URL())
	if *cms != "" {
		g.ConnectCMS(*cms)
		status, err := g.Announce(*cms)
		if err != nil {
			return fmt.Errorf("announcing to %s: %w", *cms, err)
		}
		fmt.Fprintf(out, "announced to %s: %d\n", *cms, status)
	}
	if *walk {
		c := &talq.Client{URL: g.URL(), CMSAddress: "6f1c0000-0000-4000-8000-0000000c0001", BearerToken: *token, HTTP: &http.Client{Timeout: 10 * time.Second}}
		n := 0
		failed := sim.Walk(ctx, c, func(st sim.Step) {
			n++
			if st.Err != nil {
				fmt.Fprintf(out, "walk: %s: %v\n", st.Op.ID, st.Err)
			}
		})
		fmt.Fprintf(out, "walk: %d operations, %d failed\n", n, failed)
		if failed > 0 {
			return fmt.Errorf("walk: %d operations failed", failed)
		}
	}
	if ready != nil {
		ready <- g
	}
	<-ctx.Done()
	return nil
}
