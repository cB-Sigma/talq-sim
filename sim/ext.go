package sim

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cB-Sigma/talq"
)

// LuminaireType1 is the luminaire type the simulated lamps refer to.
const LuminaireType1 = "3d2f7c10-5b8e-4f61-8a2d-1c9e0b7a6f51"

// AssetKinds are the asset type collections of TALQ 2.6 the simulator
// serves.
var AssetKinds = []string{"lamp-types", "luminaire-types", "driver-types", "controller-types", "bracket-types"}

// DALIKinds are the DALI type collections TALQ 2.7 added; the simulator
// serves them like the asset types.
var DALIKinds = []string{
	"control-device-dali-part103-types", "control-gear-dali-part102-types",
	"luminaire-data-dali-part251-types", "energy-data-dali-part252-types",
	"diagnostics-and-maintenance-data-dali-part253-types",
	"luminaire-mounted-control-device-dali-part351-types",
}

// ext is the simulator state for groups, asset types, device classes,
// paging, pulled log reports, data packages and deletions.
type ext struct {
	groups     map[string]map[string]any
	assets     map[string]map[string]map[string]any // kind → address → type
	assignedTo map[string]string                    // "resource:address" → calendar
	deleted    []string
	pages      []string
	reports    map[string]map[string]any // logger → last log report
	packages   map[string]map[string]any // packageId → data package
	served     map[string]int            // operationId → requests answered
	resyncs    int
	// PageLimit, when above zero, turns on paging of GET /devices: it is
	// the default limit and the largest page served.
	PageLimit int
	// MaxCalendars, when above zero, is the most calendars the gateway
	// keeps; more are refused with serviceConstraintExceeded.
	MaxCalendars int
}

func (g *Gateway) initExt() {
	g.groups = map[string]map[string]any{}
	g.assignedTo = map[string]string{}
	g.assets = map[string]map[string]map[string]any{}
	g.reports = map[string]map[string]any{}
	g.packages = map[string]map[string]any{}
	g.served = map[string]int{}
	for _, k := range append(append([]string(nil), AssetKinds...), DALIKinds...) {
		g.assets[k] = map[string]map[string]any{}
	}
	g.assets["luminaire-types"][LuminaireType1] = map[string]any{"address": LuminaireType1, "name": "LED 60W Type II", "lightSourceType": "LED", "maximumPower": 60.0}
	g.packages["fw-sl"] = map[string]any{"packageId": "fw-sl", "releaseId": "1.0.0", "ownerCMS": "", "downloadUri": "https://gw.example/packages/fw-sl-1.0.0"}
}

// SetPageLimit turns paging of GET /devices on (limit > 0) or off.
func (g *Gateway) SetPageLimit(n int) { g.mu.Lock(); g.PageLimit = n; g.mu.Unlock() }

// SetMaxCalendars limits the calendars kept (0: no limit).
func (g *Gateway) SetMaxCalendars(n int) { g.mu.Lock(); g.MaxCalendars = n; g.mu.Unlock() }

// AddLocal adds a lamp without telling the CMS.
func (g *Gateway) AddLocal(addr, name, class string) {
	dev := lamp(addr, name, 10)
	if class != "" {
		dev["class"] = class
	}
	g.mu.Lock()
	g.devices[addr] = dev
	g.order = append(g.order, addr)
	g.mu.Unlock()
}

// PageRequests returns the query strings of the GET /devices requests.
func (g *Gateway) PageRequests() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.pages...)
}

// Group returns a group (nil when absent).
func (g *Gateway) Group(addr string) map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.groups[addr]
}

// AssetType returns an asset type of a kind (nil when absent).
func (g *Gateway) AssetType(kind, addr string) map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.assets[kind][addr]
}

// Program returns a control program (nil when absent).
func (g *Gateway) Program(id string) map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.programs[id]
}

// Calendar returns a calendar (nil when absent).
func (g *Gateway) Calendar(id string) map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calendars[id]
}

// Logger returns a logger configuration (nil when absent).
func (g *Gateway) Logger(addr string) map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.loggers[addr]
}

// Device returns a device (nil when absent).
func (g *Gateway) Device(addr string) map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.devices[addr]
}

// AssignedTo returns the calendar assigned to "devices:<addr>" or
// "groups:<addr>".
func (g *Gateway) AssignedTo(ref string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.assignedTo[ref]
}

// Deleted returns the paths deleted by the CMS.
func (g *Gateway) Deleted() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.deleted...)
}

// Served returns how many requests of each operation (by operationId) the
// gateway answered past its checks and faults.
func (g *Gateway) Served() map[string]int {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]int, len(g.served))
	for k, v := range g.served {
		out[k] = v
	}
	return out
}

// Resyncs counts the CMS's DELETE of the gateway's own device (a request
// to start the bootstrap again).
func (g *Gateway) Resyncs() int { g.mu.Lock(); defer g.mu.Unlock(); return g.resyncs }

// expand resolves a resource reference to device addresses (groups
// recursively); ok is false when it names nothing known.
func (g *Gateway) expand(ref map[string]any) ([]string, bool) {
	return g.expandDepth(ref, 0)
}

func (g *Gateway) expandDepth(ref map[string]any, depth int) ([]string, bool) {
	addr := fmt.Sprint(ref["address"])
	switch ref["resource"] {
	case "devices":
		dev, _, _ := strings.Cut(addr, "/")
		if g.devices[dev] == nil {
			return nil, false
		}
		return []string{dev}, true
	case "groups":
		grp := g.groups[addr]
		if grp == nil || depth > 8 {
			return nil, false
		}
		var out []string
		for _, m := range asList(grp["members"]) {
			mm, _ := m.(map[string]any)
			devs, ok := g.expandDepth(mm, depth+1)
			if !ok {
				return nil, false
			}
			out = append(out, devs...)
		}
		return out, true
	}
	return nil, false
}

func qInt(r *http.Request, name string) (int, bool, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, false, true
	}
	n, err := strconv.Atoi(v)
	return n, true, err == nil && n >= 0
}

func (g *Gateway) devicesOf(classes []string) []any {
	out := []any{}
	for _, a := range g.order {
		dev := g.devices[a]
		if dev == nil {
			continue
		}
		if len(classes) > 0 && !slices.Contains(classes, fmt.Sprint(dev["class"])) {
			continue
		}
		out = append(out, dev)
	}
	return out
}

// getDevices serves GET /devices with the deviceClasses filter and, when
// paging is on and offset or limit is given, one page with the
// pagination-* headers.
func (g *Gateway) getDevices(w http.ResponseWriter, r *http.Request) {
	g.pages = append(g.pages, r.URL.RawQuery)
	out := g.devicesOf(r.URL.Query()["deviceClasses"])
	offset, hasOff, ok1 := qInt(r, "offset")
	limit, hasLim, ok2 := qInt(r, "limit")
	if !ok1 || !ok2 {
		talqErrD(w, http.StatusBadRequest, talq.KeyParameterValueNotValid, "invalidPagination")
		return
	}
	if g.PageLimit > 0 && (hasOff || hasLim) && !g.ignorePaging {
		if !hasLim || limit > g.PageLimit {
			limit = g.PageLimit
		}
		total := len(out)
		lo := min(offset, total)
		hi := min(lo+limit, total)
		out = out[lo:hi]
		w.Header().Set("pagination-count", strconv.Itoa(len(out)))
		w.Header().Set("pagination-offset", strconv.Itoa(lo))
		w.Header().Set("pagination-totalCount", strconv.Itoa(total))
		w.Header().Set("pagination-defaultLimit", strconv.Itoa(g.PageLimit))
	}
	writeJSON(w, http.StatusOK, out)
}

var (
	isoPeriod = regexp.MustCompile(`^P(\d+D)?(T(\d+H)?(\d+M)?(\d+S)?)?$`)
	gDay      = regexp.MustCompile(`^---(0[1-9]|[12]\d|3[01])$`)
	gMonthDay = regexp.MustCompile(`^--(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])$`)
)

// periodOf turns a samplingPeriod into a duration.
func periodOf(s string) time.Duration {
	m := isoPeriod.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	var d time.Duration
	for i, u := range map[int]time.Duration{1: 24 * time.Hour, 3: time.Hour, 4: time.Minute, 5: time.Second} {
		if m[i] != "" {
			n, _ := strconv.Atoi(m[i][:len(m[i])-1])
			d += time.Duration(n) * u
		}
	}
	return d
}

func validClock(v any) bool {
	s, _ := v.(string)
	_, err := time.Parse("15:04:05", s)
	return err == nil
}

// validLogger checks a logger configuration's recording and reporting
// modes; it returns an error key or "".
func validLogger(lc map[string]any) string {
	if lc["ownerCMS"] == nil {
		return "noOwner"
	}
	rec, _ := lc["recordingMode"].(map[string]any)
	switch rec["type"] {
	case "EventRecordingMode":
		if _, ok := rec["sourceEvents"].(map[string]any); !ok {
			return "noSourceEvents"
		}
	case "PeriodicRecordingMode":
		content, _ := rec["content"].([]any)
		if len(content) == 0 {
			return "noContent"
		}
		for _, c := range content {
			cm, _ := c.(map[string]any)
			f, _ := cm["function"].(map[string]any)
			if f["functionId"] == nil && f["functionType"] == nil {
				return "noFunction"
			}
		}
		profiles, _ := rec["samplingProfiles"].([]any)
		if len(profiles) == 0 {
			return "noSamplingProfiles"
		}
		for _, p := range profiles {
			pm, _ := p.(map[string]any)
			st, _ := pm["samplingStartTime"].(string)
			if _, err := time.Parse(time.RFC3339, st); err != nil {
				return "badSamplingStartTime"
			}
			if sp, ok := pm["samplingPeriod"].(string); ok && (!isoPeriod.MatchString(sp) || periodOf(sp) == 0) {
				return "badSamplingPeriod"
			}
		}
	default:
		return "badRecordingMode"
	}
	rep, _ := lc["reportingMode"].(map[string]any)
	switch rep["type"] {
	case "ImmediateReportingMode":
		if d, ok := rep["delay"].(float64); !ok || d < 0 || d != float64(int64(d)) {
			return "badDelay"
		}
	case "ScheduledReportingMode":
		times, _ := rep["times"].([]any)
		if len(times) == 0 {
			return "noTimes"
		}
		for _, t := range times {
			if !validClock(t) {
				return "badTime"
			}
		}
	default:
		return "badReportingMode"
	}
	return ""
}

func num(v any) bool { _, ok := v.(float64); return ok }

var operations = []string{"Set", "Max", "Min", "Add", "Sub", "Mul"}

// validScheduleObject checks a control program or calendar.
func (g *Gateway) validScheduleObject(path string, m map[string]any) string {
	if path == "/control-programs" {
		if m["type"] != "ControlProgramOne" {
			return "unknownProgramType"
		}
		periods, _ := m["activePeriods"].([]any)
		if len(periods) == 0 {
			return "noActivePeriods"
		}
		for _, p := range periods {
			pm, _ := p.(map[string]any)
			switch pm["name"] {
			case "AbsoluteActivePeriod":
				if !validClock(pm["startTime"]) || !validClock(pm["endTime"]) {
					return "badAbsolutePeriod"
				}
			case "AstroClockActivePeriod":
				if !num(pm["sunsetOffset"]) || !num(pm["sunriseOffset"]) {
					return "badAstroPeriod"
				}
			case "SensorActivePeriod":
				s, _ := pm["sensor"].(map[string]any)
				if a, _ := s["sensorAddresses"].([]any); len(a) == 0 || !num(pm["onLevel"]) || !num(pm["offLevel"]) {
					return "badSensorPeriod"
				}
			default:
				return "unknownActivePeriod"
			}
		}
		if _, ok := m["fixedTimeControls"].([]any); !ok {
			return "noFixedTimeControls"
		}
		for _, a := range asList(m["astroClockTimeControls"]) {
			am, _ := a.(map[string]any)
			cmd, _ := am["command"].(map[string]any)
			if (am["solarEventType"] != "sunrise" && am["solarEventType"] != "sunset") || !num(am["offset"]) ||
				!slices.Contains(operations, fmt.Sprint(am["operation"])) || cmd["state"] == nil {
				return "badAstroClockTimeControl"
			}
		}
		for _, dc := range asList(m["dynamicControls"]) {
			dm, _ := dc.(map[string]any)
			e, _ := dm["effect"].(map[string]any)
			src, _ := e["source"].(map[string]any)
			if (e["type"] != "FixedControlEffect" && e["type"] != "ExternalControlEffect") || src["sensor"] == nil ||
				!slices.Contains(operations, fmt.Sprint(e["operation"])) {
				return "badDynamicControl"
			}
		}
		return ""
	}
	if dp, ok := m["defaultProgram"].(string); ok && dp != "" && g.programs[dp] == nil {
		return "unknownDefaultProgram"
	}
	for _, r := range asList(m["rules"]) {
		rm, _ := r.(map[string]any)
		if g.programs[fmt.Sprint(rm["program"])] == nil {
			return "unknownRuleProgram"
		}
		for _, k := range []string{"startDate", "endDate"} {
			if v, ok := rm[k].(string); ok {
				if _, err := time.Parse(time.DateOnly, v); err != nil && !gMonthDay.MatchString(v) {
					return "badRuleDate"
				}
			}
		}
		cond, _ := rm["condition"].(map[string]any)
		switch cond["type"] {
		case nil:
		case "ccDate":
			dates := asList(cond["dates"])
			if len(dates) == 0 {
				return "noDates"
			}
			for _, d := range dates {
				dm, _ := d.(map[string]any)
				for _, k := range []string{"start", "end"} {
					v, has := dm[k]
					s, _ := v.(string)
					if (k == "start" || has) && !gDay.MatchString(s) && !gMonthDay.MatchString(s) {
						return "badDate"
					}
				}
			}
		case "ccDay":
			days := asList(cond["days"])
			if len(days) == 0 || len(days) > 7 {
				return "badDays"
			}
			for _, d := range days {
				dm, _ := d.(map[string]any)
				st, _ := dm["start"].(float64)
				if st < 1 || st > 7 {
					return "badDay"
				}
				if e, has := dm["end"]; has {
					if ef, _ := e.(float64); ef <= st || ef > 7 {
						return "badDay"
					}
				}
			}
			for _, o := range asList(cond["occurrences"]) {
				if s, _ := o.(string); len(s) != 1 || !strings.Contains("12345L", s) {
					return "badOccurrence"
				}
			}
		default:
			return "unknownCondition"
		}
	}
	return ""
}

func asList(v any) []any { l, _ := v.([]any); return l }

func (g *Gateway) validMembers(members []any) bool {
	for _, m := range members {
		mm, ok := m.(map[string]any)
		if !ok {
			return false
		}
		if _, ok := g.expand(mm); !ok {
			return false
		}
	}
	return true
}

func sameRef(x any, ref map[string]any) bool {
	xm, _ := x.(map[string]any)
	return xm["resource"] == ref["resource"] && xm["address"] == ref["address"]
}

// Sample sends one log report for a periodic logger with the given number
// of samples of each configured attribute of each device, timestamped
// from the first sampling profile's start by its period. The report is
// also kept for GET /log-reports/{loggerAddress}.
func (g *Gateway) Sample(cms, loggerAddr string, samples int) (int, error) {
	g.mu.Lock()
	lc := g.loggers[loggerAddr]
	if lc == nil {
		g.mu.Unlock()
		return 0, fmt.Errorf("no logger %s", loggerAddr)
	}
	rec, _ := lc["recordingMode"].(map[string]any)
	profs := asList(rec["samplingProfiles"])
	if len(profs) == 0 {
		g.mu.Unlock()
		return 0, fmt.Errorf("logger %s is not periodic", loggerAddr)
	}
	prof, _ := profs[0].(map[string]any)
	start, _ := time.Parse(time.RFC3339, fmt.Sprint(prof["samplingStartTime"]))
	period := periodOf(fmt.Sprint(prof["samplingPeriod"]))
	var white []string
	if sa, ok := lc["sourceAddresses"].(map[string]any); ok {
		for _, x := range asList(sa["whiteList"]) {
			xm, _ := x.(map[string]any)
			white = append(white, fmt.Sprint(xm["address"]))
		}
	}
	var entries []any
	for _, addr := range g.order {
		if len(white) > 0 && !slices.Contains(white, addr) {
			continue
		}
		for _, f := range asList(g.devices[addr]["functions"]) {
			fm, _ := f.(map[string]any)
			for _, c := range asList(rec["content"]) {
				cm, _ := c.(map[string]any)
				want, _ := cm["function"].(map[string]any)
				if (want["functionId"] != nil && want["functionId"] != fm["id"]) || (want["functionType"] != nil && want["functionType"] != fm["type"]) {
					continue
				}
				for _, a := range asList(want["attributes"]) {
					am, _ := a.(map[string]any)
					name := fmt.Sprint(am["name"])
					attr, ok := fm[name].(map[string]any)
					if !ok {
						continue
					}
					for k := range samples {
						v := attr["value"]
						if fv, ok := v.(float64); ok {
							v = fv + float64(k)
						}
						entries = append(entries, map[string]any{"data": map[string]any{
							"type": "AttributeLogData", "srcAddress": addr + "/" + fmt.Sprint(fm["id"]),
							"timestamp": start.Add(time.Duration(k) * period).UTC().Format(time.RFC3339), "attributeName": name,
							"attributeValue": map[string]any{"type": attr["type"], "value": v},
						}})
					}
				}
			}
		}
	}
	rep := map[string]any{"address": loggerAddr, "entries": entries}
	g.keepReport(loggerAddr, rep)
	g.mu.Unlock()
	return g.post(cms, "/log-reports", rep)
}

// keepReport keeps a logger's latest report for the pull operation
// (called with g.mu held).
func (g *Gateway) keepReport(logger string, rep map[string]any) { g.reports[logger] = rep }
