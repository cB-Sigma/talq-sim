// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package talq_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cB-Sigma/talq"
	"github.com/cB-Sigma/talq-sim/sim"
)

const (
	cms    = "6f1c0000-0000-4000-8000-0000000c0001"
	gwAddr = "6f1c0000-0000-4000-8000-00000000d001"
	newDev = sim.WalkDevice
	group1 = sim.WalkGroup
)

func object(seg string) map[string]any {
	switch seg {
	case "groups":
		return map[string]any{"address": group1, "ownerCMS": cms, "members": []any{map[string]any{"resource": "devices", "address": sim.Lamp1}}}
	case "calendars":
		return map[string]any{"id": "cal-1", "ownerCMS": cms, "defaultProgram": "prog-1"}
	}
	return map[string]any{"id": "prog-1", "ownerCMS": cms, "type": "ControlProgramOne",
		"activePeriods":     []any{map[string]any{"name": "AbsoluteActivePeriod", "startTime": "18:00:00", "endTime": "06:00:00"}},
		"fixedTimeControls": []any{}}
}

// Every operation of the TALQ 2.7.1 gateway API that the talq client can
// call is answered by the simulator with a status the definition lists,
// through the client's own Do and Count (sim.Walk).
func TestEveryGatewayOperation(t *testing.T) {
	g := sim.New(gwAddr, "gw-token")
	defer g.Close()
	c := &talq.Client{URL: g.URL(), CMSAddress: cms, BearerToken: "gw-token"}
	failed := sim.Walk(context.Background(), c, func(st sim.Step) {
		switch {
		case st.Err != nil:
			t.Errorf("%s %s: %v", st.Op.Method, st.Op.Path, st.Err)
		case st.Resp != nil && !st.Op.Accepts(st.Resp.Status):
			t.Errorf("%s answered %d, which the definition does not list (%v)", st.Op.ID, st.Resp.Status, st.Op.Codes)
		}
	})
	if failed != 0 {
		t.Errorf("%d steps failed", failed)
	}
	served := g.Served()
	for _, op := range talq.GatewayOperations {
		if served[op.ID] == 0 {
			t.Errorf("%s was not served", op.ID)
		}
	}
	if g.Level(sim.Lamp1) != 30 || g.AssignedTo("groups:"+group1) != "cal-1" || g.Device(newDev) != nil {
		t.Errorf("state after the run: level %d, assigned %q, device %v", g.Level(sim.Lamp1), g.AssignedTo("groups:"+group1), g.Device(newDev))
	}
}

// The simulator gives the TALQ error answers of the definitions, and the
// client reports them as refusals or outages.
func TestErrorAnswers(t *testing.T) {
	g := sim.New(gwAddr, "")
	defer g.Close()
	c := &talq.Client{URL: g.URL(), CMSAddress: cms}
	ctx := context.Background()
	key := func(err error) (int, string) {
		var ae *talq.APIError
		if !errors.As(err, &ae) || len(ae.Errors) == 0 {
			return 0, ""
		}
		return ae.Status, ae.Errors[0].Key
	}
	check := func(name string, err error, status int, k string) {
		t.Helper()
		if s, got := key(err); s != status || got != k {
			t.Errorf("%s: %d %q, want %d %q (%v)", name, s, got, status, k, err)
		}
	}
	_, err := c.Override(ctx, []talq.Ref{{Resource: "devices", Address: newDev}}, talq.LevelCommand(10))
	check("unknown override address", err, 422, talq.KeyOverrideAddressNotValid)
	_, err = c.Override(ctx, []talq.Ref{{Resource: "devices", Address: sim.Lamp1}}, talq.LevelCommand(140))
	check("level out of range", err, 422, talq.KeyOverrideCommandNotValid)
	_, err = c.Assign(ctx, []talq.Ref{{Resource: "devices", Address: sim.Lamp1}}, talq.Ref{Resource: "calendars", Address: "nope"}, "")
	check("unknown calendar", err, 422, talq.KeyAssignmentEntityNotValid)
	check("unknown device", c.GetDevice(ctx, newDev, nil), 404, talq.KeyResourceNotFound)
	_, err = c.Add(ctx, "luminaire-types", []any{map[string]any{"address": sim.LuminaireType1, "name": "dup"}}, nil)
	check("duplicate", err, 409, talq.KeyResourceConflict)
	_, err = c.UpdateAll(ctx, "groups", []any{object("groups")}, nil)
	check("bulk update of an unknown group", err, 404, talq.KeyResourceNotFound)
	if !talq.Refused(err) {
		t.Error("a 404 is not a refusal")
	}

	g.SetMaxCalendars(1)
	_, _ = c.Add(ctx, "control-programs", []any{object("control-programs")}, nil)
	if _, err := c.Add(ctx, "calendars", []any{object("calendars")}, nil); err != nil {
		t.Fatal(err)
	}
	second := object("calendars")
	second["id"] = "cal-2"
	_, err = c.Add(ctx, "calendars", []any{second}, nil)
	check("too many calendars", err, 422, talq.KeyServiceConstraintExceeded)
	g.SetStrictReferences(true)
	_, err = c.Delete(ctx, "control-programs", "prog-1")
	check("program in use", err, 422, talq.KeyDeletingResourceIsReferred)

	g.SetFault("getDevices", sim.Fault{Status: 429, Key: talq.KeyGenericError})
	_, err = c.ListDevices(ctx, nil, -1)
	check("fault", err, 429, talq.KeyGenericError)
	if talq.Refused(err) {
		t.Error("429 counted as a refusal")
	}
	g.SetFault("getDevices", sim.Fault{})
	g.SetFault("*", sim.Fault{Status: 500, Key: talq.KeyGenericError})
	check("fault on every operation", c.List(ctx, "groups", nil), 500, talq.KeyGenericError)
	g.SetFault("*", sim.Fault{})

	bad := &talq.Client{URL: g.URL(), CMSAddress: "not-a-uuid"}
	_, err = bad.ListDevices(ctx, nil, -1)
	check("clientAddress not a UUID", err, 400, talq.KeyParameterValueNotValid)
	versionless := &talq.Client{URL: g.URL(), CMSAddress: cms, APIVersion: " "}
	_, err = versionless.ListDevices(ctx, nil, -1)
	check("blank talq-api-version", err, 400, talq.KeyHeaderMissing)
}

// Outages, slow and hanging gateways and redirects reach the client as
// errors that are not refusals.
func TestTransportFaults(t *testing.T) {
	g := sim.New(gwAddr, "")
	defer g.Close()
	c := &talq.Client{URL: g.URL(), CMSAddress: cms}
	g.SetDown(true)
	_, err := c.ListDevices(context.Background(), nil, -1)
	var ae *talq.APIError
	if !errors.As(err, &ae) || ae.Status != 503 || talq.Refused(err) {
		t.Errorf("down: %v", err)
	}
	g.SetDown(false)
	g.SetDelay(300 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err = c.ListDevices(ctx, nil, -1)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("slow gateway: %v", err)
	}
	if _, err := c.ListDevices(context.Background(), nil, -1); err != nil {
		t.Errorf("slow gateway with time to answer: %v", err)
	}
	g.SetDelay(0)
	g.SetHang(true)
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err = c.ListDevices(ctx, nil, -1)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("hanging gateway: %v", err)
	}
	g.SetHang(false)
	g.SetRedirectWrites(true)
	nofollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	rc := &talq.Client{URL: g.URL(), CMSAddress: cms, HTTP: nofollow}
	if _, err := rc.Override(context.Background(), []talq.Ref{{Resource: "devices", Address: sim.Lamp1}}, talq.LevelCommand(5)); err == nil ||
		!strings.Contains(err.Error(), "moved") {
		t.Errorf("redirect: %v", err)
	}
	if g.Level(sim.Lamp1) == 5 {
		t.Error("a redirected override was applied")
	}
}

// fakeCMS serves the whole CMS API with talq.Serve and records the
// operations it was called with; addDevices from a gateway with the nil
// address is answered with the gateway address the CMS assigns.
type fakeCMS struct {
	mu   sync.Mutex
	seen map[string]int
	srv  *httptest.Server
}

func newFakeCMS(t *testing.T) *fakeCMS {
	f := &fakeCMS{seen: map[string]int{}}
	f.srv = httptest.NewServer(talq.Serve("/cms", talq.CMSOperations, func(w http.ResponseWriter, r *http.Request, op talq.Operation, params map[string]string, body []byte) {
		f.mu.Lock()
		f.seen[op.ID]++
		f.mu.Unlock()
		switch {
		case op.ID == "addDevices" && strings.Contains(string(body), talq.NilUUID):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`[{"address":"` + gwAddr + `","name":"gw","class":"gateway","functions":[{"id":"gateway","type":"GatewayFunction","gatewayAddress":{"type":"AttributeString","value":"` + gwAddr + `"}}]}]`))
		case op.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
		default:
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(op.Path, "/count") {
				_, _ = w.Write([]byte("0"))
				return
			}
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCMS) URL() string { return f.srv.URL + "/cms" }

// The simulated gateway can call every operation of the CMS API, which
// the library's Serve routes; the bootstrap takes the address the CMS
// assigns; 202 answers are followed by a PATCH /devices to the CMS.
func TestGatewayCallsEveryCMSOperation(t *testing.T) {
	f := newFakeCMS(t)
	g := sim.New(talq.NilUUID, "")
	defer g.Close()
	ctx := context.Background()
	if err := g.Bootstrap(ctx, f.URL()); err != nil {
		t.Fatal(err)
	}
	if g.Address != gwAddr {
		t.Errorf("address after bootstrap %s", g.Address)
	}
	for _, op := range talq.CMSOperations {
		params := map[string]string{}
		for _, p := range op.Params() {
			params[p] = "x/" + p
		}
		var body any
		if op.Writes() && op.Method != http.MethodDelete {
			body = []any{}
		}
		if _, err := g.CallCMS(ctx, f.URL(), op.ID, params, nil, body, nil); err != nil {
			t.Errorf("%s: %v", op.ID, err)
		}
	}
	f.mu.Lock()
	for _, op := range talq.CMSOperations {
		if f.seen[op.ID] == 0 {
			t.Errorf("%s never reached the CMS", op.ID)
		}
	}
	before := f.seen["partialUpdateDevices"]
	f.mu.Unlock()

	g.ConnectCMS(f.URL())
	g.SetAccepted(true)
	c := &talq.Client{URL: g.URL(), CMSAddress: cms}
	resp, err := c.Override(ctx, []talq.Ref{{Resource: "devices", Address: sim.Lamp2}}, talq.LevelCommand(70))
	if err != nil || resp.Status != http.StatusAccepted {
		t.Fatalf("override: %v %v", resp, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		n := f.seen["partialUpdateDevices"]
		f.mu.Unlock()
		if n > before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no PATCH /devices after the 202")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if g.Level(sim.Lamp2) != 70 || g.PatchesSent() != 1 {
		t.Errorf("level %d, patches %d", g.Level(sim.Lamp2), g.PatchesSent())
	}
}

// Paging: the client follows a paging gateway's pages, and a gateway
// that does not page answers everything to the first request.
// The protocol-level fault switches: counts in text/plain, a gateway that
// advertises paging and ignores it, 403 while bootstrapping, 307 and 301
// redirects on any method.
func TestProtocolFaults(t *testing.T) {
	g := sim.New(gwAddr, "")
	defer g.Close()
	c := &talq.Client{URL: g.URL(), CMSAddress: cms}
	ctx := context.Background()
	g.SetCountText(true)
	if n, err := c.CountDevices(ctx, nil); err != nil || n != 2 {
		t.Errorf("text/plain count: %d %v", n, err)
	}
	g.SetPageLimit(1)
	g.SetIgnorePaging(true)
	devs, p, err := c.DevicesPage(ctx, nil, 0, 1)
	if err != nil || p.Paged || len(devs) != 2 {
		t.Errorf("ignored paging: %d %+v %v", len(devs), p, err)
	}
	if all, err := c.ListDevices(ctx, nil, 1); err != nil || len(all) != 2 {
		t.Errorf("ListDevices on a gateway ignoring paging: %d %v", len(all), err)
	}
	g.SetBootstrapping(true)
	var ae *talq.APIError
	if _, err := c.ListDevices(ctx, nil, -1); !errors.As(err, &ae) || ae.Status != 403 {
		t.Errorf("bootstrapping: %v", err)
	}
	g.SetBootstrapping(false)
	g.SetRedirectAll(http.StatusTemporaryRedirect)
	if _, err := c.Override(ctx, []talq.Ref{{Resource: "devices", Address: sim.Lamp1}}, talq.LevelCommand(12)); err != nil || g.Level(sim.Lamp1) != 12 {
		t.Errorf("307 keeps the method and body: %v, level %d", err, g.Level(sim.Lamp1))
	}
	g.SetRedirectAll(http.StatusMovedPermanently)
	if _, err := c.Override(ctx, []talq.Ref{{Resource: "devices", Address: sim.Lamp1}}, talq.LevelCommand(13)); err == nil || g.Level(sim.Lamp1) == 13 {
		t.Errorf("a write turned into a GET by a 301 was not reported: %v", err)
	}
	if devs, err := c.ListDevices(ctx, nil, -1); err != nil || len(devs) != 2 {
		t.Errorf("GET through a 301: %v", err)
	}
}

func TestPaging(t *testing.T) {
	g := sim.New(gwAddr, "")
	defer g.Close()
	for i := range 5 {
		g.AddLocal("6f1c0000-0000-4000-8000-0000000001"+string(rune('a'+i))+"0", "SL-extra", "")
	}
	c := &talq.Client{URL: g.URL(), CMSAddress: cms}
	ctx := context.Background()
	devs, err := c.ListDevices(ctx, nil, 3)
	if err != nil || len(devs) != 7 || len(g.PageRequests()) != 1 {
		t.Errorf("non-paging: %d devices in %d requests, %v", len(devs), len(g.PageRequests()), err)
	}
	g.SetPageLimit(2)
	devs, err = c.ListDevices(ctx, nil, 3)
	if err != nil || len(devs) != 7 || len(g.PageRequests()) != 5 {
		t.Errorf("paging: %d devices in %d requests, %v", len(devs), len(g.PageRequests()), err)
	}
	_, p, err := c.DevicesPage(ctx, nil, 6, 2)
	if err != nil || p != (talq.Page{Paged: true, Count: 1, Offset: 6, TotalCount: 7, DefaultLimit: 2}) {
		t.Errorf("last page %+v %v", p, err)
	}
	if n, err := c.CountDevices(ctx, []string{"streetlightController"}); err != nil || n != 7 {
		t.Errorf("count %d %v", n, err)
	}
}

func selfSigned(t *testing.T, cn string, isCA bool) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, IsCA: isCA, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}, cert
}

// Mutual TLS: a gateway that requires a client certificate accepts a CMS
// with MTLSClient and its certificate, and refuses one without.
func TestMutualTLS(t *testing.T) {
	gwCert, gwX := selfSigned(t, "gateway", true)
	cmsCert, cmsX := selfSigned(t, "cms", true)
	clientCAs := x509.NewCertPool()
	clientCAs.AddCert(cmsX)
	g := sim.NewTLS(gwAddr, "", &tls.Config{Certificates: []tls.Certificate{gwCert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientCAs})
	defer g.Close()
	roots := x509.NewCertPool()
	roots.AddCert(gwX)
	c := &talq.Client{URL: g.URL(), CMSAddress: cms, HTTP: talq.MTLSClient(cmsCert, roots)}
	if _, err := c.ListDevices(context.Background(), nil, -1); err != nil {
		t.Fatalf("with a client certificate: %v", err)
	}
	stranger, _ := selfSigned(t, "stranger", true)
	c.HTTP = talq.MTLSClient(stranger, roots)
	if _, err := c.ListDevices(context.Background(), nil, -1); err == nil {
		t.Error("a client certificate from an unknown issuer was accepted")
	}
}
