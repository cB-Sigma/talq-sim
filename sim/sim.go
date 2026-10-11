// Package sim simulates a TALQ 2.x gateway for tests: every operation of
// the TALQ 2.7.1 gateway API a CMS calls (services, device classes,
// devices with paging, functions and attributes, override and assign
// commands, groups and their members, logger configurations and pulled
// log reports, control programs, calendars, every asset type including
// the DALI types, data packages, the counts), the gateway's client side
// towards a CMS (bootstrap, services, devices, log reports, any CMS API
// operation), and faults on demand (outages, redirects, delays, hangs,
// error answers per operation, 202 answers with the results sent later).
package sim

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cB-Sigma/talq"
)

// Gateway is a simulated TALQ gateway with streetlight controllers.
type Gateway struct {
	srv *httptest.Server
	// Address is the gateway's TALQ address.
	Address string
	// Token is the bearer token the CMS must send (empty: none).
	Token string
	// CMSToken is sent to the CMS.
	CMSToken string

	mu        sync.Mutex
	devices   map[string]map[string]any // address → device
	order     []string
	loggers   map[string]map[string]any
	programs  map[string]map[string]any
	calendars map[string]map[string]any
	assigned  []map[string]any
	overrides []map[string]any
	cmsURL    string
	versions  []string
	down      bool
	redirect  bool // answer writes with 301 (SetRedirectWrites)

	ext
	faults
}

// Lamp devices the simulator starts with.
const (
	Lamp1 = "0b6f8a52-1d0e-4c55-9a0b-6c1b2d3e4f01"
	Lamp2 = "0b6f8a52-1d0e-4c55-9a0b-6c1b2d3e4f02"
)

func lamp(addr, name string, level int) map[string]any {
	return map[string]any{
		"address": addr, "name": name, "class": "streetlightController",
		"functions": []any{
			map[string]any{"id": "basic", "type": "BasicFunction",
				"serial":   map[string]any{"type": "AttributeString", "value": "SL-" + addr[len(addr)-2:]},
				"location": map[string]any{"type": "AttributeLocation", "value": map[string]any{"latitude": 18.52, "longitude": 73.85}}},
			map[string]any{"id": "lampActuator", "type": "LampActuatorFunction",
				"actualLightState": map[string]any{"type": "AttributeLevelState", "timestamp": "2026-09-24T12:00:00Z",
					"value": map[string]any{"name": "LevelState", "value": level}}},
			map[string]any{"id": "lampMonitor", "type": "LampMonitorFunction",
				"activePower":    map[string]any{"type": "AttributeFloat", "timestamp": "2026-09-24T12:00:00Z", "value": 41.5},
				"operatingHours": map[string]any{"type": "AttributeFloat", "value": 1234.0}},
			map[string]any{"id": "luminaireAsset", "type": "LuminaireAssetFunction",
				"luminaireTypeAddress": map[string]any{"type": "AttributeString", "value": LuminaireType1}},
		},
	}
}

// New starts a gateway with two lamps on a loopback port.
func New(address, token string) *Gateway {
	g := newGateway(address, token)
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	return g
}

// NewAt starts a gateway with two lamps listening on addr.
func NewAt(listen, address, token string) (*Gateway, error) {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	g := newGateway(address, token)
	g.srv = httptest.NewUnstartedServer(http.HandlerFunc(g.serve))
	_ = g.srv.Listener.Close()
	g.srv.Listener = ln
	g.srv.Start()
	return g, nil
}

// NewTLS starts a gateway with two lamps on a loopback port over TLS with
// cfg (set Certificates, and ClientAuth with ClientCAs for the mutual TLS
// TALQ expects). Without a certificate in cfg, httptest's own is used.
func NewTLS(address, token string, cfg *tls.Config) *Gateway {
	g := newGateway(address, token)
	g.srv = httptest.NewUnstartedServer(http.HandlerFunc(g.serve))
	g.srv.TLS = cfg
	g.srv.StartTLS()
	return g
}

func newGateway(address, token string) *Gateway {
	g := &Gateway{Address: address, Token: token, devices: map[string]map[string]any{},
		loggers: map[string]map[string]any{}, programs: map[string]map[string]any{}, calendars: map[string]map[string]any{}}
	g.devices[Lamp1], g.devices[Lamp2] = lamp(Lamp1, "SL-Ward7-001", 100), lamp(Lamp2, "SL-Ward7-002", 0)
	g.initExt()
	g.order = []string{Lamp1, Lamp2}
	return g
}

// URL is the gateway API base.
func (g *Gateway) URL() string { return g.srv.URL + "/talq" }

// HTTPClient is an HTTP client that trusts the gateway's TLS certificate
// (for NewTLS without a certificate of the test's own).
func (g *Gateway) HTTPClient() *http.Client { return g.srv.Client() }

// Close stops the gateway.
func (g *Gateway) Close() { g.srv.Close() }

// SetRedirectWrites makes the gateway answer every request other than GET
// with 301 to a page a GET reads with 200 (an http to https move, or a
// load balancer's landing page), as a misconfigured gateway URL does.
func (g *Gateway) SetRedirectWrites(v bool) { g.mu.Lock(); g.redirect = v; g.mu.Unlock() }

// SetDown makes the gateway answer 503.
func (g *Gateway) SetDown(v bool) { g.mu.Lock(); g.down = v; g.mu.Unlock() }

// Overrides returns the override commands received.
func (g *Gateway) Overrides() []map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]map[string]any(nil), g.overrides...)
}

// Loggers returns the logger configurations installed.
func (g *Gateway) Loggers() int { g.mu.Lock(); defer g.mu.Unlock(); return len(g.loggers) }

// Schedule returns the programs, calendars and assignments installed.
func (g *Gateway) Schedule() (programs, calendars int, assigned []map[string]any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.programs), len(g.calendars), append([]map[string]any(nil), g.assigned...)
}

// APIVersions returns the talq-api-version headers seen.
func (g *Gateway) APIVersions() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.versions...)
}

// Level returns a lamp's actual level.
func (g *Gateway) Level(addr string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return levelOf(g.devices[addr])
}

func levelOf(dev map[string]any) int {
	if dev == nil {
		return -1
	}
	for _, f := range asList(dev["functions"]) {
		fm, _ := f.(map[string]any)
		if fm["id"] == "lampActuator" {
			a, _ := fm["actualLightState"].(map[string]any)
			st, _ := a["value"].(map[string]any)
			switch v := st["value"].(type) {
			case int:
				return v
			case float64:
				return int(v)
			}
		}
	}
	return -1
}

func talqErr(w http.ResponseWriter, status int, key string) {
	talq.WriteError(w, status, key, "")
}

func talqErrD(w http.ResponseWriter, status int, key, desc string) {
	talq.WriteError(w, status, key, desc)
}

func (g *Gateway) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-transform")
	g.mu.Lock()
	down, redirect, delay, hang := g.down, g.redirect, g.delay, g.hang
	g.versions = append(g.versions, r.Header.Get("talq-api-version"))
	g.mu.Unlock()
	if hang {
		<-r.Context().Done()
		return
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if redirect && r.Method != http.MethodGet {
		http.Redirect(w, r, "/talq/devices?"+r.URL.RawQuery, http.StatusMovedPermanently)
		return
	}
	g.mu.Lock()
	redirectAll, booting := g.redirectAll, g.booting
	g.mu.Unlock()
	path := r.URL.EscapedPath()
	if moved, ok := strings.CutPrefix(path, "/moved"); ok {
		path = moved
	} else if redirectAll != 0 {
		w.Header().Set("Location", "/moved"+path+"?"+r.URL.RawQuery)
		w.WriteHeader(redirectAll)
		return
	}
	if booting {
		talq.WriteBootstrapping(w)
		return
	}
	rest, ok := strings.CutPrefix(path, "/talq")
	if !ok {
		talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, "the gateway API is at /talq")
		return
	}
	op, params, ok := talq.Match(talq.GatewayOperations, r.Method, rest)
	if !ok {
		talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, r.Method+" "+r.URL.Path)
		return
	}
	if status, e := talq.CheckRequest(r, op); e != nil {
		talqErrD(w, status, e.Key, e.Description)
		return
	}
	if g.Token != "" && r.Header.Get("Authorization") != "Bearer "+g.Token {
		talqErrD(w, http.StatusUnauthorized, talq.KeyGenericError, "unauthorized")
		return
	}
	if f, ok := g.fault(op.ID); ok {
		talqErrD(w, f.Status, f.Key, "simulated fault")
		return
	}
	raw, err := talq.RequestBody(r, 32<<20)
	if err == talq.ErrUnsupportedEncoding {
		talqErrD(w, http.StatusUnsupportedMediaType, talq.KeyHeaderValueNotValid, "Content-Encoding")
		return
	}
	var body any
	if len(bytes.TrimSpace(raw)) > 0 && json.Unmarshal(raw, &body) != nil {
		talqErrD(w, http.StatusBadRequest, talq.KeyPayloadError, "the body is not JSON")
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.served[op.ID]++
	g.handle(w, r, op, params, body)
}

// handle answers one checked request (called with g.mu held).
func (g *Gateway) handle(w http.ResponseWriter, r *http.Request, op talq.Operation, params map[string]string, body any) {
	switch op.ID {
	case "getDevices":
		g.getDevices(w, r)
	case "getDeviceAttribute":
		dev := g.devices[params["deviceAddress"]]
		if dev == nil {
			talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, "unknownDevice")
			return
		}
		if fm := function(dev, params["functionId"]); fm != nil {
			if a, ok := fm[params["attributeName"]]; ok && params["attributeName"] != "id" && params["attributeName"] != "type" {
				writeJSON(w, http.StatusOK, a)
				return
			}
		}
		talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, "unknownAttribute")
	case "sendCommand":
		g.override(w, op, body)
	case "addLoggerConfig":
		lc, _ := body.(map[string]any)
		addr, _ := lc["address"].(string)
		if addr == "" {
			talqErrD(w, http.StatusUnprocessableEntity, talq.KeyPayloadError, "noAddress")
			return
		}
		if _, dup := g.loggers[addr]; dup {
			talqErrD(w, http.StatusConflict, talq.KeyResourceConflict, "exists")
			return
		}
		if key := validLogger(lc); key != "" {
			talqErrD(w, http.StatusUnprocessableEntity, talq.KeyPayloadError, key)
			return
		}
		g.loggers[addr] = lc
		w.WriteHeader(http.StatusCreated)
	case "updateLoggerConfig":
		lc, _ := body.(map[string]any)
		if _, ok := g.loggers[params["loggerAddress"]]; !ok {
			talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, "unknownLogger")
			return
		}
		if key := validLogger(lc); key != "" {
			talqErrD(w, http.StatusUnprocessableEntity, talq.KeyPayloadError, key)
			return
		}
		g.loggers[params["loggerAddress"]] = lc
		w.WriteHeader(http.StatusOK)
	case "addControlPrograms", "addCalendars":
		g.addSchedule(w, op, body)
	case "updateControlProgram", "updateCalendar":
		kind := "/calendars"
		addr := params["calendarAddress"]
		if op.ID == "updateControlProgram" {
			kind, addr = "/control-programs", params["controlProgramAddress"]
		}
		m, _ := body.(map[string]any)
		if key := g.validScheduleObject(kind, m); key != "" {
			talqErrD(w, http.StatusUnprocessableEntity, talq.KeyPayloadError, key)
			return
		}
		if kind == "/calendars" {
			g.calendars[addr] = m
		} else {
			g.programs[addr] = m
		}
		writeJSON(w, http.StatusOK, m)
	case "assignCommand":
		g.assign(w, op, body)
	default:
		if g.serveExt(w, r, op, params, body) {
			return
		}
		talqErrD(w, http.StatusNotImplemented, talq.KeyGenericError, "not simulated: "+op.ID)
	}
}

func function(dev map[string]any, id string) map[string]any {
	for _, f := range asList(dev["functions"]) {
		fm, _ := f.(map[string]any)
		if fm["id"] == id {
			return fm
		}
	}
	return nil
}

func (g *Gateway) override(w http.ResponseWriter, op talq.Operation, body any) {
	oc, _ := body.(map[string]any)
	addrs, _ := oc["addresses"].([]any)
	if len(addrs) == 0 {
		talqErrD(w, http.StatusUnprocessableEntity, talq.KeyOverrideAddressNotValid, "noAddresses")
		return
	}
	var targets []string
	for _, a := range addrs {
		am, _ := a.(map[string]any)
		devs, ok := g.expand(am)
		if !ok {
			talqErrD(w, http.StatusUnprocessableEntity, talq.KeyOverrideAddressNotValid, "unknownAddress")
			return
		}
		targets = append(targets, devs...)
	}
	tc, _ := oc["targetCommand"].(map[string]any)
	level := -1
	if tc != nil {
		st, _ := tc["state"].(map[string]any)
		v, ok := st["value"].(float64)
		if !ok || v < 0 || v > 100 {
			talqErrD(w, http.StatusUnprocessableEntity, talq.KeyOverrideCommandNotValid, "targetCommand.state.value")
			return
		}
		level = int(v)
	}
	g.overrides = append(g.overrides, oc)
	for _, devAddr := range targets {
		lv := level
		if lv < 0 {
			lv = 100 // release: back to the default state
		}
		g.setLevel(devAddr, lv)
	}
	g.done(w, op, http.StatusCreated, nil, targets)
}

func (g *Gateway) addSchedule(w http.ResponseWriter, op talq.Operation, body any) {
	path, store := "/control-programs", g.programs
	if op.ID == "addCalendars" {
		path, store = "/calendars", g.calendars
	}
	list := asList(body)
	for _, x := range list {
		m, _ := x.(map[string]any)
		id, _ := m["id"].(string)
		if id == "" || m["ownerCMS"] == nil {
			talqErrD(w, http.StatusUnprocessableEntity, talq.KeyPayloadError, "missingIdOrOwner")
			return
		}
		if key := g.validScheduleObject(path, m); key != "" {
			talqErrD(w, http.StatusUnprocessableEntity, talq.KeyPayloadError, key)
			return
		}
		if _, dup := store[id]; dup {
			talqErrD(w, http.StatusConflict, talq.KeyResourceConflict, "exists")
			return
		}
	}
	if g.MaxCalendars > 0 && op.ID == "addCalendars" && len(store)+len(list) > g.MaxCalendars {
		talqErrD(w, http.StatusUnprocessableEntity, talq.KeyServiceConstraintExceeded, "maximumCalendars")
		return
	}
	for _, x := range list {
		m := x.(map[string]any)
		store[m["id"].(string)] = m
	}
	writeJSON(w, http.StatusCreated, list)
}

func (g *Gateway) assign(w http.ResponseWriter, op talq.Operation, body any) {
	ac, _ := body.(map[string]any)
	ent, _ := ac["entity"].(map[string]any)
	if ent == nil || ent["resource"] != "calendars" || (ent["address"] != "" && g.calendars[fmt.Sprint(ent["address"])] == nil) {
		talqErrD(w, http.StatusUnprocessableEntity, talq.KeyAssignmentEntityNotValid, "unknownEntity")
		return
	}
	addrs, _ := ac["addresses"].([]any)
	if len(addrs) == 0 {
		talqErrD(w, http.StatusUnprocessableEntity, talq.KeyAssignmentAddressNotValid, "noAddresses")
		return
	}
	var targets []string
	for _, a := range addrs {
		am, _ := a.(map[string]any)
		devs, ok := g.expand(am)
		if !ok {
			talqErrD(w, http.StatusUnprocessableEntity, talq.KeyAssignmentAddressNotValid, "unknownAddress")
			return
		}
		targets = append(targets, devs...)
	}
	for _, a := range addrs {
		am := a.(map[string]any)
		g.assignedTo[fmt.Sprint(am["resource"])+":"+fmt.Sprint(am["address"])] = fmt.Sprint(ent["address"])
	}
	g.assigned = append(g.assigned, ac)
	g.done(w, op, http.StatusCreated, nil, targets)
}

// setLevel changes a lamp's level and reports it to the CMS when a logger
// is installed (called with g.mu held).
func (g *Gateway) setLevel(addr string, level int) {
	dev := g.devices[addr]
	if fm := function(dev, "lampActuator"); fm != nil {
		fm["actualLightState"] = map[string]any{"type": "AttributeLevelState", "timestamp": time.Now().UTC().Format(time.RFC3339),
			"value": map[string]any{"name": "LevelState", "value": level}}
	}
	if len(g.loggers) == 0 {
		return
	}
	var logger string
	for a := range g.loggers {
		logger = a
	}
	rep := map[string]any{"address": logger, "entries": []any{map[string]any{"data": map[string]any{
		"type": "AttributeLogData", "srcAddress": addr + "/lampActuator", "timestamp": time.Now().UTC().Format(time.RFC3339),
		"attributeName": "actualLightState", "attributeValue": map[string]any{"type": "AttributeLevelState", "value": map[string]any{"name": "LevelState", "value": level}},
	}}}}
	g.keepReport(logger, rep)
	if g.cmsURL == "" {
		return
	}
	cms := g.cmsURL
	go func() { _, _ = g.post(cms, "/log-reports", rep) }()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ConnectCMS records the CMS API base for reports.
func (g *Gateway) ConnectCMS(cmsURL string) {
	g.mu.Lock()
	g.cmsURL = cmsURL
	g.mu.Unlock()
}

func (g *Gateway) post(cms, path string, v any) (int, error) {
	b, _ := json.Marshal(v)
	q := url.Values{"clientAddress": {g.Address}, "talqRequestId": {talq.NewRequestID()}}
	req, err := http.NewRequest(http.MethodPost, cms+path+"?"+q.Encode(), bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("talq-api-version", "2.7.1")
	req.Header.Set("Content-Type", "application/json")
	if g.CMSToken != "" {
		req.Header.Set("Authorization", "Bearer "+g.CMSToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// Announce posts the gateway's services to the CMS.
func (g *Gateway) Announce(cms string) (int, error) {
	return g.post(cms, "/services", g.services())
}

// AddDevice adds a lamp and posts it to the CMS.
func (g *Gateway) AddDevice(cms, addr, name string) (int, error) {
	dev := lamp(addr, name, 50)
	g.mu.Lock()
	g.devices[addr] = dev
	g.order = append(g.order, addr)
	g.mu.Unlock()
	return g.post(cms, "/devices", []any{dev})
}

// Event posts an event log report to the CMS.
func (g *Gateway) Event(cms, device, eventType string, start bool) (int, error) {
	return g.post(cms, "/log-reports", map[string]any{"address": "logger", "entries": []any{map[string]any{"data": map[string]any{
		"type": "EventLogData", "srcAddress": device + "/lampMonitor", "timestamp": time.Now().UTC().Format(time.RFC3339),
		"eventType": eventType, "startEndFlag": start, "info": []any{map[string]any{"description": "lamp does not light", "lang": "en"}},
	}}}})
}

// Raw posts any body to a CMS path (for malformed-request tests).
func (g *Gateway) Raw(cms, path string, v any) (int, error) { return g.post(cms, path, v) }

// CMSClient is a client of a CMS's API acting as this gateway: its
// address as clientAddress, CMSToken as bearer token and the CMS API's
// operations, for CallCMS and any other call the tests want.
func (g *Gateway) CMSClient(cms string) *talq.Client {
	return &talq.Client{URL: cms, CMSAddress: g.Address, BearerToken: g.CMSToken, Ops: talq.CMSOperations}
}

// CallCMS runs any CMS API operation (by its operationId) against cms as
// this gateway.
func (g *Gateway) CallCMS(ctx context.Context, cms, id string, params map[string]string, query url.Values, in, out any) (*talq.Response, error) {
	return g.CMSClient(cms).Do(ctx, id, params, query, in, out)
}
