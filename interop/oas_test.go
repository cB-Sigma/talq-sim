// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package interop checks the simulator and the talq client against the
// TALQ consortium's own OpenAPI definitions (TALQ 2.7.1, BSD-3-Clause):
// every request body the client sends, every answer the simulated gateway
// gives and every request it sends to a CMS is validated against the
// schema the definitions give for that operation and status. The
// definitions are not kept here: set SIGMA_TALQ_OAS to the oas directory
// of a TALQ_specification checkout, or to "fetch" to download the three
// files at the pinned commit (their SHA-256 sums are checked).
package interop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cB-Sigma/talq"
	"github.com/cB-Sigma/talq-sim/sim"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The pinned files: github.com/TALQ-consortium/TALQ_specification at
// commit b05f9a2c8df945fceab5f47c0ebc045724dc77e0, directory oas/.
const commit = "b05f9a2c8df945fceab5f47c0ebc045724dc77e0"

var files = map[string]string{
	"talq-api-cms-2-7-1-online.json":     "48006a6718ce1807f1bc87c7c9eb61b82b2bd8d03128b1ec8f753a52863fba6f",
	"talq-api-gateway-2-7-1-online.json": "102cf1bde7201720c45ba7bb09442340b787ad414a78b87db961024296295ef6",
	"talq-data-model-2-7-1-online.json":  "ac0e2b0d0e9ccd7aa6d6501a5e6e73b1f47fa937a15ec804f1e67dc159f32609",
}

const base = "https://talq.invalid/oas/"

const (
	cms    = "6f1c0000-0000-4000-8000-0000000c0001"
	gwAddr = "6f1c0000-0000-4000-8000-00000000d001"
)

// definitions loads the three files into a schema compiler.
func definitions(t *testing.T) (*jsonschema.Compiler, map[string]map[string]any) {
	t.Helper()
	src := os.Getenv("SIGMA_TALQ_OAS")
	if src == "" {
		t.Skip("set SIGMA_TALQ_OAS to a TALQ_specification oas directory, or to fetch")
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft4)
	docs := map[string]map[string]any{}
	for name, sum := range files {
		var b []byte
		var err error
		if src == "fetch" {
			b, err = fetch("https://raw.githubusercontent.com/TALQ-consortium/TALQ_specification/" + commit + "/oas/" + name)
		} else {
			b, err = os.ReadFile(filepath.Join(src, name))
		}
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != sum {
			t.Fatalf("%s: SHA-256 %x, want %s (not the pinned file)", name, h, sum)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.AddResource(base+name, doc); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		docs[name] = m
	}
	return c, docs
}

func fetch(u string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// pointer is the JSON pointer of a path item's member.
func pointer(tokens ...string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteString("/" + strings.ReplaceAll(strings.ReplaceAll(t, "~", "~0"), "/", "~1"))
	}
	return (&url.URL{Fragment: b.String()}).EscapedFragment()
}

type checker struct {
	t       *testing.T
	c       *jsonschema.Compiler
	docs    map[string]map[string]any
	checked int
}

// has reports whether the definition has a JSON schema at the path.
func (k *checker) has(file string, tokens ...string) bool {
	var v any = k.docs[file]
	for _, tok := range tokens {
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		v, ok = m[tok]
		if !ok {
			return false
		}
	}
	return v != nil
}

// check validates body (JSON) against the schema at tokens of file, when
// the definition has one there.
func (k *checker) check(what, file string, body []byte, tokens ...string) {
	k.t.Helper()
	if !k.has(file, tokens...) || len(bytes.TrimSpace(body)) == 0 {
		return
	}
	s, err := k.c.Compile(base + file + "#" + pointer(tokens...))
	if err != nil {
		k.t.Fatalf("%s: compiling the schema: %v", what, err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		k.t.Errorf("%s: not JSON: %v", what, err)
		return
	}
	if err := s.Validate(inst); err != nil {
		k.t.Errorf("%s: does not match the TALQ schema: %v\n%s", what, err, body)
		return
	}
	k.checked++
}

// recorder keeps the last exchange of an HTTP client.
type recorder struct {
	mu        sync.Mutex
	req, resp []byte
	status    int
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil {
		reqBody, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(b))
	r.mu.Lock()
	r.req, r.resp, r.status = reqBody, b, resp.StatusCode
	r.mu.Unlock()
	return resp, nil
}

// The client's request bodies and the simulated gateway's answers to
// every gateway operation match the TALQ 2.7.1 schemas, as do the error
// bodies the simulator gives.
func TestGatewayAgainstDefinitions(t *testing.T) {
	c, docs := definitions(t)
	k := &checker{t: t, c: c, docs: docs}
	const gwFile = "talq-api-gateway-2-7-1-online.json"
	g := sim.New(gwAddr, "")
	defer g.Close()
	rec := &recorder{}
	cl := &talq.Client{URL: g.URL(), CMSAddress: cms, HTTP: &http.Client{Transport: rec}}
	answers := 0
	sim.Walk(context.Background(), cl, func(st sim.Step) {
		if st.Err != nil {
			t.Errorf("%s: %v", st.Op.ID, st.Err)
			return
		}
		rec.mu.Lock()
		req, resp, status := rec.req, rec.resp, rec.status
		rec.mu.Unlock()
		m := strings.ToLower(st.Op.Method)
		k.check(st.Op.ID+" request", gwFile, req, "paths", st.Op.Path, m, "requestBody", "content", "application/json", "schema")
		before := k.checked
		k.check(st.Op.ID+" answer", gwFile, resp, "paths", st.Op.Path, m, "responses", strconv.Itoa(status), "content", "application/json", "schema")
		answers += k.checked - before
	})
	// Error answers: an unknown device, a duplicate, a semantic error.
	for _, f := range []func() error{
		func() error { return cl.GetDevice(context.Background(), sim.WalkDevice, nil) },
		func() error {
			_, err := cl.Add(context.Background(), "luminaire-types", []any{map[string]any{"address": sim.LuminaireType1, "name": "x"}}, nil)
			return err
		},
		func() error {
			_, err := cl.Override(context.Background(), nil, talq.LevelCommand(1))
			return err
		},
	} {
		if f() == nil {
			t.Fatal("an error answer was expected")
		}
		rec.mu.Lock()
		var msgs []json.RawMessage
		_ = json.Unmarshal(rec.resp, &msgs)
		for _, m := range msgs {
			k.check("error message", "talq-data-model-2-7-1-online.json", m, "components", "schemas", "TALQErrorMessage")
		}
		rec.mu.Unlock()
	}
	t.Logf("%d bodies matched their TALQ schema (%d gateway answers)", k.checked, answers)
	if answers < 40 {
		t.Errorf("only %d answers had a schema to check", answers)
	}
}

// What the simulated gateway sends to a CMS (bootstrap, announcement,
// devices, events, periodic log reports, the PATCH after a 202) matches
// the CMS API's request schemas.
func TestGatewayRequestsToCMSAgainstDefinitions(t *testing.T) {
	c, docs := definitions(t)
	k := &checker{t: t, c: c, docs: docs}
	const cmsFile = "talq-api-cms-2-7-1-online.json"
	var mu sync.Mutex
	type got struct {
		op   talq.Operation
		body []byte
	}
	var seen []got
	srv := httptest.NewServer(talq.Serve("/cms", talq.CMSOperations, func(w http.ResponseWriter, r *http.Request, op talq.Operation, params map[string]string, body []byte) {
		mu.Lock()
		seen = append(seen, got{op, body})
		mu.Unlock()
		if op.ID == "addDevices" && bytes.Contains(body, []byte(talq.NilUUID)) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`[{"address":"` + gwAddr + `","name":"gw","class":"gateway","functions":[{"id":"gateway","type":"GatewayFunction","gatewayAddress":{"type":"AttributeString","value":"` + gwAddr + `"}}]}]`))
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	cmsURL := srv.URL + "/cms"
	g := sim.New(talq.NilUUID, "")
	defer g.Close()
	ctx := context.Background()
	if err := g.Bootstrap(ctx, cmsURL); err != nil {
		t.Fatal(err)
	}
	g.ConnectCMS(cmsURL)
	if _, err := g.AddDevice(cmsURL, "6f1c0000-0000-4000-8000-0000000000e1", "SL-e1"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Event(cmsURL, sim.Lamp1, "lampFailure", true); err != nil {
		t.Fatal(err)
	}
	cl := &talq.Client{URL: g.URL(), CMSAddress: cms}
	lc := map[string]any{"address": "6f1c0000-0000-4000-8000-0000000b0002", "ownerCMS": cms,
		"recordingMode": map[string]any{"type": "PeriodicRecordingMode",
			"content":          []any{map[string]any{"function": map[string]any{"functionId": "lampMonitor", "attributes": []any{map[string]any{"name": "activePower"}}}}},
			"samplingProfiles": []any{map[string]any{"samplingStartTime": "2026-10-01T00:00:00Z", "samplingPeriod": "PT15M"}}},
		"reportingMode": map[string]any{"type": "ImmediateReportingMode", "delay": 0}}
	if _, err := cl.Do(ctx, "addLoggerConfig", nil, nil, lc, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Sample(cmsURL, "6f1c0000-0000-4000-8000-0000000b0002", 3); err != nil {
		t.Fatal(err)
	}
	g.SetAccepted(true)
	if _, err := cl.Override(ctx, []talq.Ref{{Resource: "devices", Address: sim.Lamp1}}, talq.LevelCommand(20)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := 0
		for _, s := range seen {
			if s.op.ID == "partialUpdateDevices" {
				n++
			}
		}
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	ids := map[string]bool{}
	for _, s := range seen {
		ids[s.op.ID] = true
		k.check(s.op.ID+" to the CMS", cmsFile, s.body, "paths", s.op.Path, strings.ToLower(s.op.Method), "requestBody", "content", "application/json", "schema")
	}
	for _, id := range []string{"addDevices", "addDeviceClasses", "partialUpdateDevice", "announceServices", "sendLogReport", "partialUpdateDevices"} {
		if !ids[id] {
			t.Errorf("the gateway never sent %s", id)
		}
	}
	t.Logf("%d bodies sent to the CMS matched their TALQ schema", k.checked)
}
