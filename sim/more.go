// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cB-Sigma/talq"
)

// Fault is an error answer the gateway gives to every request of one
// operation: an HTTP status with a TALQErrorMessage key.
type Fault struct {
	Status int
	Key    string
}

// faults is the fault state: per-operation error answers, a delay or a
// hang before every answer, 202 answers.
type faults struct {
	byOp     map[string]Fault
	delay    time.Duration
	hang     bool
	accepted bool
	strict   bool
	patches  int
	// redirectAll, when not 0, redirects every request with that status.
	redirectAll  int
	countText    bool
	ignorePaging bool
	booting      bool
}

// writeCount answers a count in JSON or, with SetCountText, text/plain
// (called with g.mu held).
func (g *Gateway) writeCount(w http.ResponseWriter, n int) {
	if g.countText {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "%d", n)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

// SetRedirectAll redirects every request (any method) with status (301,
// 302, 307 or 308) to the same path and query under /moved/talq, where
// the gateway answers as usual; 0 turns it off. A client following a 307
// keeps its method and body; following a 301 turns a write into a GET.
func (g *Gateway) SetRedirectAll(status int) { g.mu.Lock(); g.redirectAll = status; g.mu.Unlock() }

// SetCountText makes the count operations answer in text/plain instead
// of JSON (both are allowed).
func (g *Gateway) SetCountText(v bool) { g.mu.Lock(); g.countText = v; g.mu.Unlock() }

// SetIgnorePaging makes the gateway advertise paging (SetPageLimit) but
// answer GET /devices with the whole list and no pagination headers.
func (g *Gateway) SetIgnorePaging(v bool) { g.mu.Lock(); g.ignorePaging = v; g.mu.Unlock() }

// SetBootstrapping makes the gateway answer every request with 403, as a
// TALQ peer does while a bootstrap is not finished.
func (g *Gateway) SetBootstrapping(v bool) { g.mu.Lock(); g.booting = v; g.mu.Unlock() }

// SetFault makes every request of the operation (an operationId of
// talq.GatewayOperations, or "*" for all) answer f after the protocol
// checks; a zero Fault removes it.
func (g *Gateway) SetFault(op string, f Fault) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.byOp == nil {
		g.byOp = map[string]Fault{}
	}
	if f.Status == 0 {
		delete(g.byOp, op)
		return
	}
	g.byOp[op] = f
}

func (g *Gateway) fault(op string) (Fault, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if f, ok := g.byOp[op]; ok {
		return f, true
	}
	f, ok := g.byOp["*"]
	return f, ok
}

// SetDelay delays every answer by d (a slow gateway; a client's timeout
// shorter than d fails).
func (g *Gateway) SetDelay(d time.Duration) { g.mu.Lock(); g.delay = d; g.mu.Unlock() }

// SetHang makes the gateway accept requests and never answer them (until
// the client gives up).
func (g *Gateway) SetHang(v bool) { g.mu.Lock(); g.hang = v; g.mu.Unlock() }

// SetAccepted makes the operations that may answer 202 (devices,
// functions, attributes, override and assign commands) answer 202 with no
// body; the change is made at once and, when a CMS is connected, the
// affected devices are sent to it with PATCH /devices.
func (g *Gateway) SetAccepted(v bool) { g.mu.Lock(); g.accepted = v; g.mu.Unlock() }

// SetStrictReferences refuses (422 deletingResourceIsReferred) deleting a
// control program a calendar uses, a calendar that is assigned, and an
// asset type a device refers to.
func (g *Gateway) SetStrictReferences(v bool) { g.mu.Lock(); g.strict = v; g.mu.Unlock() }

// PatchesSent counts the PATCH /devices requests sent to the CMS after
// 202 answers.
func (g *Gateway) PatchesSent() int { g.mu.Lock(); defer g.mu.Unlock(); return g.patches }

// done answers a write: 202 when SetAccepted is on and the operation may
// answer it (the devices are sent to the CMS later), else status with
// body (none when nil). Called with g.mu held.
func (g *Gateway) done(w http.ResponseWriter, op talq.Operation, status int, body any, devices []string) {
	if g.accepted && op.Accepts(http.StatusAccepted) {
		w.WriteHeader(http.StatusAccepted)
		if g.cmsURL == "" || len(devices) == 0 {
			return
		}
		var devs []any
		for _, d := range dedupe(devices) {
			if dev := g.devices[d]; dev != nil {
				devs = append(devs, dev)
			}
		}
		g.patches++
		cms := g.cmsURL
		c := g.CMSClient(cms)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = c.Do(ctx, "partialUpdateDevices", nil, nil, devs, nil)
		}()
		return
	}
	if body == nil {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, body)
}

func dedupe(s []string) []string {
	var out []string
	for _, v := range s {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// services are the services the gateway offers (GET /services and the
// announcement).
func (g *Gateway) services() []any {
	return []any{
		map[string]any{"name": "ControlService", "events": []any{}},
		map[string]any{"name": "DataCollectService", "events": []any{}},
		map[string]any{"name": "ConfigurationService", "events": []any{map[string]any{"type": "lampFailure"}},
			"devicesPaginationSupported": g.PageLimit > 0},
		map[string]any{"name": "GroupManagementService", "events": []any{}},
		map[string]any{"name": "AssetManagementService", "events": []any{}},
		map[string]any{"name": "DataPackageService", "events": []any{}},
		map[string]any{"name": "DaliProtocolService", "events": []any{}},
	}
}

func (g *Gateway) deviceClasses() []any {
	seen := map[string]bool{}
	out := []any{}
	for _, a := range g.order {
		dev := g.devices[a]
		if dev == nil {
			continue
		}
		cl := fmt.Sprint(dev["class"])
		if seen[cl] {
			continue
		}
		seen[cl] = true
		var fns []any
		for _, f := range asList(dev["functions"]) {
			fm, _ := f.(map[string]any)
			fns = append(fns, map[string]any{"functionId": fm["id"], "type": fm["type"]})
		}
		out = append(out, map[string]any{"name": cl, "functions": fns})
	}
	return out
}

// store returns the collection behind a path's first segment and the
// property its objects are keyed by.
func (g *Gateway) store(seg string) (map[string]map[string]any, string, bool) {
	switch seg {
	case "groups":
		return g.groups, "address", true
	case "calendars":
		return g.calendars, "id", true
	case "control-programs":
		return g.programs, "id", true
	}
	s, ok := g.assets[seg]
	return s, "address", ok
}

func sorted(m map[string]map[string]any) []any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []any{}
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

// checkObject checks one object written to a collection; it returns an
// error key or "".
func (g *Gateway) checkObject(seg string, m map[string]any) string {
	if m == nil {
		return "notAnObject"
	}
	switch seg {
	case "groups":
		members, ok := m["members"].([]any)
		addr, _ := m["address"].(string)
		if addr == "" || m["ownerCMS"] == nil || !ok {
			return "missingAddressOwnerOrMembers"
		}
		if !g.validMembers(members) {
			return "unknownMember"
		}
	case "calendars", "control-programs":
		if id, _ := m["id"].(string); id == "" || m["ownerCMS"] == nil {
			return "missingIdOrOwner"
		}
		return g.validScheduleObject("/"+seg, m)
	default:
		if addr, _ := m["address"].(string); addr == "" || m["name"] == nil {
			return "missingAddressOrName"
		}
		if seg == "lamp-types" && m["controlType"] == nil {
			return "missingControlType"
		}
	}
	return ""
}

// referred reports whether an object about to be deleted is still used
// (only with SetStrictReferences).
func (g *Gateway) referred(seg, addr string) bool {
	if !g.strict {
		return false
	}
	switch seg {
	case "control-programs":
		for _, c := range g.calendars {
			if c["defaultProgram"] == addr {
				return true
			}
			for _, r := range asList(c["rules"]) {
				if rm, _ := r.(map[string]any); rm["program"] == addr {
					return true
				}
			}
		}
	case "calendars":
		for _, v := range g.assignedTo {
			if v == addr {
				return true
			}
		}
	default:
		for _, dev := range g.devices {
			for _, f := range asList(dev["functions"]) {
				fm, _ := f.(map[string]any)
				for _, v := range fm {
					if a, ok := v.(map[string]any); ok && a["value"] == addr {
						return true
					}
				}
			}
		}
	}
	return false
}

// serveCollection serves the list, bulk, count and item operations of
// groups, calendars, control programs and every asset and DALI type.
func (g *Gateway) serveCollection(w http.ResponseWriter, op talq.Operation, params map[string]string, body any) bool {
	seg, rest, _ := strings.Cut(strings.TrimPrefix(op.Path, "/"), "/")
	store, key, ok := g.store(seg)
	if !ok {
		return false
	}
	var addr string
	if ps := op.Params(); len(ps) == 1 {
		addr = params[ps[0]]
	}
	switch {
	case op.Method == http.MethodGet && rest == "":
		writeJSON(w, http.StatusOK, sorted(store))
	case op.Method == http.MethodGet && rest == "count":
		g.writeCount(w, len(store))
	case op.Method == http.MethodPost && rest == "":
		if seg == "calendars" || seg == "control-programs" {
			return false // addSchedule
		}
		list := asList(body)
		for _, x := range list {
			m, _ := x.(map[string]any)
			if k := g.checkObject(seg, m); k != "" {
				talqErrD(w, http.StatusUnprocessableEntity, talq.KeyPayloadError, k)
				return true
			}
			if store[fmt.Sprint(m[key])] != nil {
				talqErrD(w, http.StatusConflict, talq.KeyResourceConflict, "exists")
				return true
			}
		}
		for _, x := range list {
			m := x.(map[string]any)
			store[fmt.Sprint(m[key])] = m
		}
		writeJSON(w, http.StatusCreated, list)
	case op.Method == http.MethodPut && rest == "":
		list := asList(body)
		for _, x := range list {
			m, _ := x.(map[string]any)
			if store[fmt.Sprint(m[key])] == nil {
				talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, fmt.Sprint(m[key]))
				return true
			}
			if k := g.checkObject(seg, m); k != "" {
				talqErrD(w, http.StatusUnprocessableEntity, talq.KeyPayloadError, k)
				return true
			}
		}
		for _, x := range list {
			m := x.(map[string]any)
			store[fmt.Sprint(m[key])] = m
		}
		writeJSON(w, http.StatusOK, list)
	case store[addr] == nil:
		talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, "unknown "+strings.TrimSuffix(seg, "s"))
	case op.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, store[addr])
	case op.Method == http.MethodPut:
		m, _ := body.(map[string]any)
		if m == nil || fmt.Sprint(m[key]) != addr {
			talqErrD(w, http.StatusUnprocessableEntity, talq.KeyPayloadError, key+" does not match the path")
			return true
		}
		if k := g.checkObject(seg, m); k != "" {
			talqErrD(w, http.StatusUnprocessableEntity, talq.KeyPayloadError, k)
			return true
		}
		store[addr] = m
		writeJSON(w, http.StatusOK, m)
	case op.Method == http.MethodDelete:
		if g.referred(seg, addr) {
			talqErrD(w, http.StatusUnprocessableEntity, talq.KeyDeletingResourceIsReferred, addr)
			return true
		}
		delete(store, addr)
		g.deleted = append(g.deleted, "/"+seg+"/"+addr)
		w.WriteHeader(http.StatusOK)
	default:
		return false
	}
	return true
}

// mergeFunctions merges a partial device's functions into dev by id,
// attribute by attribute.
func mergeFunctions(dev, part map[string]any) {
	for k, v := range part {
		if k != "functions" && k != "address" {
			dev[k] = v
		}
	}
	for _, pf := range asList(part["functions"]) {
		pm, _ := pf.(map[string]any)
		fm := function(dev, fmt.Sprint(pm["id"]))
		if fm == nil {
			dev["functions"] = append(asList(dev["functions"]), pm)
			continue
		}
		for k, v := range pm {
			fm[k] = v
		}
	}
}

// serveExt serves the operations handle does not (called with g.mu held);
// it reports whether it answered.
func (g *Gateway) serveExt(w http.ResponseWriter, r *http.Request, op talq.Operation, params map[string]string, body any) bool {
	dev := g.devices[params["deviceAddress"]]
	switch op.ID {
	case "getServices":
		writeJSON(w, http.StatusOK, g.services())
	case "getService":
		for _, s := range g.services() {
			if s.(map[string]any)["name"] == params["serviceName"] {
				writeJSON(w, http.StatusOK, s)
				return true
			}
		}
		talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, "unknown service")
	case "getDeviceClasses":
		writeJSON(w, http.StatusOK, g.deviceClasses())
	case "getDeviceClassesCount":
		g.writeCount(w, len(g.deviceClasses()))
	case "getDeviceClass":
		for _, c := range g.deviceClasses() {
			if c.(map[string]any)["name"] == params["className"] {
				writeJSON(w, http.StatusOK, c)
				return true
			}
		}
		talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, "unknown device class")
	case "getDevicesCount":
		g.writeCount(w, len(g.devicesOf(r.URL.Query()["deviceClasses"])))
	case "getDevice":
		if dev == nil {
			talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, "unknownDevice")
			return true
		}
		writeJSON(w, http.StatusOK, dev)
	case "addDevices":
		list := asList(body)
		var addrs []string
		for _, x := range list {
			m, _ := x.(map[string]any)
			a, _ := m["address"].(string)
			if !talq.IsUUID(a) || m["name"] == nil || m["class"] == nil {
				talqErrD(w, http.StatusUnprocessableEntity, talq.KeyPayloadError, "address, name and class are required")
				return true
			}
			if g.devices[a] != nil {
				talqErrD(w, http.StatusConflict, talq.KeyResourceConflict, a)
				return true
			}
			addrs = append(addrs, a)
		}
		for _, x := range list {
			m := x.(map[string]any)
			if m["functions"] == nil {
				m["functions"] = []any{}
			}
			a := m["address"].(string)
			g.devices[a] = m
			g.order = append(g.order, a)
		}
		g.done(w, op, http.StatusCreated, list, addrs)
	case "updateDevices", "partialUpdateDevices":
		list := asList(body)
		var addrs []string
		for _, x := range list {
			m, _ := x.(map[string]any)
			a := fmt.Sprint(m["address"])
			if g.devices[a] == nil {
				talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, a)
				return true
			}
			addrs = append(addrs, a)
		}
		for _, x := range list {
			m := x.(map[string]any)
			a := fmt.Sprint(m["address"])
			if op.ID == "updateDevices" {
				g.devices[a] = m
			} else {
				mergeFunctions(g.devices[a], m)
			}
		}
		g.done(w, op, http.StatusOK, list, addrs)
	case "partialUpdateDevice":
		if dev == nil {
			if params["deviceAddress"] != g.Address {
				talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, "unknownDevice")
				return true
			}
			dev = map[string]any{"address": g.Address, "name": "gateway", "class": "gateway", "functions": []any{}}
			g.devices[g.Address] = dev
		}
		m, _ := body.(map[string]any)
		mergeFunctions(dev, m)
		g.done(w, op, http.StatusOK, dev, []string{params["deviceAddress"]})
	case "deleteDevice":
		a := params["deviceAddress"]
		if a == g.Address {
			g.resyncs++
			w.WriteHeader(http.StatusOK)
			return true
		}
		if dev == nil {
			talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, "unknownDevice")
			return true
		}
		delete(g.devices, a)
		g.order = slices.DeleteFunc(g.order, func(s string) bool { return s == a })
		g.deleted = append(g.deleted, "/devices/"+a)
		g.done(w, op, http.StatusOK, dev, nil)
	case "getDeviceFunction", "partialUpdateFunction", "partialUpdateAttribute":
		var fm map[string]any
		if dev != nil {
			fm = function(dev, params["functionId"])
		}
		if fm == nil {
			talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, "unknown device or function")
			return true
		}
		switch op.ID {
		case "getDeviceFunction":
			scope := r.URL.Query().Get("attributeScope")
			if scope == "" {
				writeJSON(w, http.StatusOK, fm)
				return true
			}
			out := map[string]any{}
			for k, v := range fm {
				a, isAttr := v.(map[string]any)
				if !isAttr || a["scope"] == scope {
					out[k] = v
				}
			}
			writeJSON(w, http.StatusOK, out)
		case "partialUpdateFunction":
			m, _ := body.(map[string]any)
			for k, v := range m {
				if k != "id" && k != "type" {
					fm[k] = v
				}
			}
			g.done(w, op, http.StatusOK, fm, []string{params["deviceAddress"]})
		default:
			name := params["attributeName"]
			if name == "id" || name == "type" {
				talqErrD(w, http.StatusUnprocessableEntity, talq.KeyPayloadError, "not an attribute")
				return true
			}
			m, _ := body.(map[string]any)
			if m == nil {
				talqErrD(w, http.StatusUnprocessableEntity, talq.KeyPayloadError, "the attribute must be an object")
				return true
			}
			fm[name] = m
			g.done(w, op, http.StatusOK, m, []string{params["deviceAddress"]})
		}
	case "getGroupMembersCount", "addGroupMembers", "deleteGroupMember":
		grp := g.groups[params["groupAddress"]]
		if grp == nil {
			talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, "unknownGroup")
			return true
		}
		members := asList(grp["members"])
		switch op.ID {
		case "getGroupMembersCount":
			g.writeCount(w, len(members))
		case "addGroupMembers":
			add := asList(body)
			if len(add) == 0 || !g.validMembers(add) {
				talqErrD(w, http.StatusUnprocessableEntity, talq.KeyRelatedResourceNotFound, "unknownMember")
				return true
			}
			for _, a := range add {
				am := a.(map[string]any)
				if !slices.ContainsFunc(members, func(x any) bool { return sameRef(x, am) }) {
					members = append(members, am)
				}
			}
			grp["members"] = members
			writeJSON(w, http.StatusOK, members)
		default:
			ref := map[string]any{"resource": params["memberResource"], "address": params["memberAddress"]}
			i := slices.IndexFunc(members, func(x any) bool { return sameRef(x, ref) })
			if i < 0 {
				talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, "notAMember")
				return true
			}
			grp["members"] = slices.Delete(members, i, i+1)
			w.WriteHeader(http.StatusOK)
		}
	case "getLoggerConfig":
		lc := g.loggers[params["loggerAddress"]]
		if lc == nil {
			talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, "unknownLogger")
			return true
		}
		writeJSON(w, http.StatusOK, lc)
	case "deleteLoggerConfig":
		if g.loggers[params["loggerAddress"]] == nil {
			talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, "notFound")
			return true
		}
		delete(g.loggers, params["loggerAddress"])
		g.deleted = append(g.deleted, "/logger-configs/"+params["loggerAddress"])
		w.WriteHeader(http.StatusOK)
	case "getLogReport":
		rep := g.reports[params["loggerAddress"]]
		if rep == nil {
			if g.loggers[params["loggerAddress"]] == nil {
				talqErrD(w, http.StatusNotFound, talq.KeyResourceNotFound, "unknownLogger")
				return true
			}
			rep = map[string]any{"address": params["loggerAddress"], "entries": []any{}}
		}
		writeJSON(w, http.StatusOK, rep)
	case "getLogReportsCount":
		g.writeCount(w, len(g.reports))
	case "getAssignCommands":
		writeJSON(w, http.StatusOK, append([]map[string]any{}, g.assigned...))
	case "getDataPackages":
		writeJSON(w, http.StatusOK, sorted(g.packages))
	case "upgradeDataPackages":
		list := asList(body)
		for _, x := range list {
			m, _ := x.(map[string]any)
			if m["packageId"] == nil || m["releaseId"] == nil || m["ownerCMS"] == nil {
				talqErrD(w, http.StatusUnprocessableEntity, talq.KeyPayloadError, "packageId, releaseId and ownerCMS are required")
				return true
			}
		}
		for _, x := range list {
			m := x.(map[string]any)
			g.packages[fmt.Sprint(m["packageId"])] = m
		}
		writeJSON(w, http.StatusOK, list)
	default:
		return g.serveCollection(w, op, params, body)
	}
	return true
}

// Bootstrap registers the gateway with a CMS the way a gateway starts:
// it posts itself with the nil address (the CMS answers with the
// gatewayAddress it assigns, which becomes Address), its device classes,
// a PATCH of its own device, its services, and an empty device class
// list to end. It returns the first error.
func (g *Gateway) Bootstrap(ctx context.Context, cms string) error {
	c := g.CMSClient(cms)
	gw := map[string]any{"address": talq.NilUUID, "name": "talqsim gateway", "class": "gateway",
		"functions": []any{map[string]any{"id": "gateway", "type": "GatewayFunction"}}}
	var created []map[string]any
	if _, err := c.Do(ctx, "addDevices", nil, nil, []any{gw}, &created); err != nil {
		return fmt.Errorf("registering: %w", err)
	}
	for _, d := range created {
		if fm := function(d, "gateway"); fm != nil {
			if a, ok := fm["gatewayAddress"].(map[string]any); ok && talq.IsUUID(fmt.Sprint(a["value"])) {
				g.mu.Lock()
				g.Address = fmt.Sprint(a["value"])
				g.mu.Unlock()
			}
		}
	}
	c = g.CMSClient(cms)
	g.mu.Lock()
	classes := g.deviceClasses()
	services := g.services()
	g.mu.Unlock()
	if _, err := c.Do(ctx, "addDeviceClasses", nil, nil, classes, nil); err != nil {
		return fmt.Errorf("device classes: %w", err)
	}
	if _, err := c.Do(ctx, "partialUpdateDevice", map[string]string{"deviceAddress": g.Address}, nil,
		map[string]any{"address": g.Address, "name": "talqsim gateway", "class": "gateway",
			"functions": []any{map[string]any{"id": "gateway", "type": "GatewayFunction"}}}, nil); err != nil {
		return fmt.Errorf("gateway device: %w", err)
	}
	if _, err := c.Do(ctx, "announceServices", nil, nil, services, nil); err != nil {
		return fmt.Errorf("services: %w", err)
	}
	if _, err := c.Do(ctx, "addDeviceClasses", nil, nil, []any{}, nil); err != nil {
		return fmt.Errorf("ending: %w", err)
	}
	return nil
}
