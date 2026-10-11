// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/cB-Sigma/talq"
)

// Addresses Walk creates on a fresh simulated gateway.
const (
	WalkGroup  = "6f1c0000-0000-4000-8000-0000000a0001"
	WalkLogger = "6f1c0000-0000-4000-8000-0000000b0001"
	WalkDevice = "6f1c0000-0000-4000-8000-0000000000d3"
)

// WalkTypeAddress is the made-up address Walk gives the type it creates
// in a type collection ("lamp-types", "control-gear-dali-part102-types"...).
func WalkTypeAddress(collection string) string {
	return fmt.Sprintf("6f1c0000-0000-4000-8000-%012x", crc32.ChecksumIEEE([]byte(collection)))
}

// Step is one operation Walk ran: what it sent and what came back.
type Step struct {
	Op     talq.Operation
	Params map[string]string
	Query  url.Values
	Body   any
	Count  int // the answer of a count operation
	Resp   *talq.Response
	Err    error
}

func walkObject(seg, owner string) map[string]any {
	switch seg {
	case "groups":
		return map[string]any{"address": WalkGroup, "ownerCMS": owner, "members": []any{map[string]any{"resource": "devices", "address": Lamp1}}}
	case "calendars":
		return map[string]any{"id": "cal-1", "ownerCMS": owner, "defaultProgram": "prog-1"}
	case "control-programs":
		return map[string]any{"id": "prog-1", "ownerCMS": owner, "type": "ControlProgramOne",
			"activePeriods":     []any{map[string]any{"name": "AbsoluteActivePeriod", "startTime": "18:00:00", "endTime": "06:00:00"}},
			"fixedTimeControls": []any{}}
	case "lamp-types":
		return map[string]any{"address": WalkTypeAddress(seg), "name": "type " + seg, "controlType": "DALI"}
	}
	return map[string]any{"address": WalkTypeAddress(seg), "name": "type " + seg}
}

// walkArgs gives the path parameters, query and body to run one gateway
// operation against a fresh simulated gateway's state.
func walkArgs(op talq.Operation, owner string) (map[string]string, url.Values, any) {
	params := map[string]string{}
	seg, _, _ := strings.Cut(strings.TrimPrefix(op.Path, "/"), "/")
	for _, p := range op.Params() {
		switch p {
		case "deviceAddress":
			params[p] = Lamp1
			if op.ID == "deleteDevice" {
				params[p] = WalkDevice
			}
		case "functionId":
			params[p] = "lampActuator"
		case "attributeName":
			params[p] = "actualLightState"
		case "groupAddress":
			params[p] = WalkGroup
		case "memberResource":
			params[p] = "devices"
		case "memberAddress":
			params[p] = Lamp2
		case "loggerAddress":
			params[p] = WalkLogger
		case "serviceName":
			params[p] = "ControlService"
		case "className":
			params[p] = "streetlightController"
		case "calendarAddress":
			params[p] = "cal-1"
		case "controlProgramAddress":
			params[p] = "prog-1"
		default:
			params[p] = WalkTypeAddress(seg)
		}
	}
	level := map[string]any{"type": "AttributeLevelState", "value": map[string]any{"name": "LevelState", "value": 30}}
	logger := map[string]any{"address": WalkLogger, "ownerCMS": owner,
		"recordingMode": map[string]any{"type": "EventRecordingMode", "sourceEvents": map[string]any{"blackList": []any{}}},
		"reportingMode": map[string]any{"type": "ImmediateReportingMode", "delay": 0}}
	switch op.ID {
	case "addDevices":
		return params, nil, []any{map[string]any{"address": WalkDevice, "name": "SL-new", "class": "streetlightController", "functions": []any{}}}
	case "updateDevices":
		return params, nil, []any{map[string]any{"address": WalkDevice, "name": "SL-renamed", "class": "streetlightController", "functions": []any{}}}
	case "partialUpdateDevices":
		return params, nil, []any{map[string]any{"address": Lamp2, "name": "SL-Ward7-002", "class": "streetlightController",
			"functions": []any{map[string]any{"id": "basic", "type": "BasicFunction"}}}}
	case "partialUpdateDevice":
		return params, nil, map[string]any{"address": Lamp1, "name": "SL-Ward7-001", "class": "streetlightController", "functions": []any{}}
	case "partialUpdateFunction":
		return params, nil, map[string]any{"id": "lampActuator", "type": "LampActuatorFunction", "actualLightState": level}
	case "partialUpdateAttribute":
		return params, nil, level
	case "getDeviceFunction":
		return params, url.Values{"attributeScope": {"status"}}, nil
	case "sendCommand":
		tc, _ := talq.OverrideTarget(40, "", nil, "walk")
		return params, nil, map[string]any{"addresses": []any{talq.Ref{Resource: "devices", Address: Lamp1}}, "targetCommand": tc}
	case "assignCommand":
		return params, nil, map[string]any{"addresses": []any{talq.Ref{Resource: "groups", Address: WalkGroup}}, "entity": talq.Ref{Resource: "calendars", Address: "cal-1"}}
	case "addLoggerConfig", "updateLoggerConfig":
		return params, nil, logger
	case "addGroupMembers":
		return params, nil, []any{talq.Ref{Resource: "devices", Address: Lamp2}}
	case "upgradeDataPackages":
		return params, nil, []any{map[string]any{"packageId": "fw-sl", "releaseId": "1.1.0", "ownerCMS": owner}}
	}
	if op.Method == http.MethodPost || (op.Method == http.MethodPut && len(op.Params()) == 0) {
		return params, nil, []any{walkObject(seg, owner)}
	}
	if op.Method == http.MethodPut {
		return params, nil, walkObject(seg, owner)
	}
	return params, nil, nil
}

// walkPhase orders the operations: creations first (programs before the
// calendars that use them), then reads and changes, then deletions (group
// members before groups).
func walkPhase(op talq.Operation) int {
	switch {
	case op.ID == "addControlPrograms":
		return 0
	case op.ID == "assignCommand":
		return 2 // after the calendar exists
	case op.Method == http.MethodPost:
		return 1
	case op.ID == "deleteGroupMember":
		return 3
	case op.Method == http.MethodDelete:
		return 4
	}
	return 2
}

// Walk runs every operation of talq.GatewayOperations once with c against
// a freshly started simulated gateway (c.URL is the gateway's URL), in an
// order that makes each one succeed: it creates a group, a logger, a
// program, a calendar, a device and one type of every type collection,
// reads, changes, counts and assigns them, sends an override, and deletes
// what it created. each, unless nil, sees every step. It returns the
// number of steps that failed. talqsim -walk and the tests use it.
func Walk(ctx context.Context, c *talq.Client, each func(Step)) int {
	ops := slices.Clone(talq.GatewayOperations)
	slices.SortStableFunc(ops, func(a, b talq.Operation) int { return walkPhase(a) - walkPhase(b) })
	failed := 0
	for _, op := range ops {
		params, query, body := walkArgs(op, c.CMSAddress)
		st := Step{Op: op, Params: params, Query: query, Body: body}
		if strings.HasSuffix(op.Path, "/count") {
			st.Count, st.Err = c.Count(ctx, op.ID, params, query)
		} else {
			st.Resp, st.Err = c.Do(ctx, op.ID, params, query, body, nil)
		}
		if st.Err != nil {
			failed++
		}
		if each != nil {
			each(st)
		}
	}
	return failed
}
