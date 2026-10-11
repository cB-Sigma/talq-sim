// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/cB-Sigma/talq"
	"github.com/cB-Sigma/talq-sim/sim"
)

// The library's client talks to the runner's gateway: device list, an
// override command, and a TALQ error for a wrong token.
func TestClientAgainstRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan *sim.Gateway, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-listen", "127.0.0.1:0", "-token", "gw-token", "-walk"}, &bytes.Buffer{}, ready)
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	var g *sim.Gateway
	select {
	case g = <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	c := &talq.Client{URL: g.URL(), CMSAddress: "6f1c0000-0000-4000-8000-0000000c0001", BearerToken: "gw-token"}
	devs, err := c.ListDevices(ctx, nil, 1)
	if err != nil || len(devs) != 2 {
		t.Fatalf("devices: %v %d", err, len(devs))
	}
	tc, err := talq.OverrideTarget(40, "", nil, "ref-1")
	if err != nil {
		t.Fatal(err)
	}
	oc := map[string]any{"addresses": []any{talq.Ref{Resource: "devices", Address: sim.Lamp1}}, "targetCommand": tc}
	if err := c.Call(ctx, http.MethodPost, "/override-commands", oc, nil); err != nil {
		t.Fatal(err)
	}
	if g.Level(sim.Lamp1) != 40 {
		t.Errorf("level = %d, want 40", g.Level(sim.Lamp1))
	}
	bad := *c
	bad.BearerToken = "wrong"
	if _, err := bad.ListDevices(ctx, nil, -1); err == nil || talq.Refused(err) {
		t.Errorf("wrong token: %v", err)
	}
}
