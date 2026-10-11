# talq-sim

A simulated TALQ 2.x (2.7.1) gateway that answers every operation of the gateway API, for testing TALQ central management systems without a gateway; not affiliated with the TALQ Consortium.

[![Go Reference](https://pkg.go.dev/badge/github.com/cB-Sigma/talq-sim.svg)](https://pkg.go.dev/github.com/cB-Sigma/talq-sim) [![test](https://github.com/cB-Sigma/talq-sim/actions/workflows/test.yml/badge.svg)](https://github.com/cB-Sigma/talq-sim/actions/workflows/test.yml) [![Licence: Apache-2.0](https://img.shields.io/badge/licence-Apache--2.0-blue.svg)](LICENSE) [![Go](https://img.shields.io/badge/go-1.22-00ADD8.svg)](go.mod)

The gateway serves the gateway API at `/talq` with two streetlight
controllers, checks every request the way TALQ asks (version header,
`clientAddress`, request IDs on writes, JSON and content encodings,
optionally a bearer token or a client certificate), keeps devices,
groups, loggers, programs, calendars and every asset and DALI type in
memory, and acts as a gateway towards a CMS: bootstrap, services,
devices, events and log reports. Faults can be switched on per
operation. It is built on [talq](https://github.com/cB-Sigma/talq).
Written for SigmaM2M, a oneM2M platform; it has no dependency on it.

## What is simulated

| Feature | Status |
|---|---|
| Every one of the 129 operations of the 2.7.1 gateway API, answered with a status the definition lists (`Walk` runs them all) | yes |
| Services (with `devicesPaginationSupported`), device classes, counts in JSON or text/plain (`SetCountText`) | yes |
| Devices: list with the `deviceClasses` filter, paging with the four `pagination-*` headers (`SetPageLimit`) or no paging, add, replace, PATCH (lists and one device), delete, functions with `attributeScope`, attributes read and PATCHed | yes |
| Override commands changing the lamps' levels (a release goes back to 100), assign commands, groups resolved recursively | yes |
| Groups and members, calendars, control programs, lamp, luminaire, driver, controller and bracket types, the six DALI types: list, add, bulk update (a whole list fails on one unknown item), count, get, update, delete | yes |
| Checks of logger configurations (recording and reporting modes as the schemas require), control programs (active periods, astro and dynamic controls) and calendars (rules, date and day conditions) | yes |
| Logger configurations; log reports sent to the CMS on level changes and by `Sample`, and kept for GET `/log-reports/{logger}` | yes |
| Data packages listed and upgraded | yes |
| Towards a CMS: `Bootstrap` (nil address, the CMS-assigned address taken, device classes, PATCH of its own device, services, the closing empty class list), `Announce` (POST `/services`), `AddDevice` (POST `/devices`), `Event` (an event log record by POST `/log-reports`; TALQ has no events endpoint), `Sample` (periodic log records by POST `/log-reports`), PATCH `/devices` after 202 answers, `CallCMS` for any of the 98 CMS API operations | yes |
| The CMS's resync (DELETE of the gateway's own device) counted (`Resyncs`) | yes |
| Faults: an error answer with any status and key for one operation or all (`SetFault`; this is how 429 or 5xx are made), 503 (`SetDown`), 403 while bootstrapping (`SetBootstrapping`), slow answers (`SetDelay`), no answer (`SetHang`), 301 on writes only (`SetRedirectWrites`), 301, 302, 307 or 308 on every method (`SetRedirectAll`), paging advertised but ignored (`SetIgnorePaging`), 202 with the results sent later as PATCH `/devices` to the CMS (`SetAccepted`), too many calendars (`SetMaxCalendars`), deleting something still used (`SetStrictReferences`), 401 for a wrong token, 400, 404, 409, 411, 415 and 422 with the TALQ error keys | yes |
| TLS with a required client certificate (`NewTLS`) | yes |
| 204 and 206 answers, data package downloads with Range, answers in the request's Content-Encoding, a gateway's own retry timing, real lamp behaviour (ramps, failures over time) | no |

## Install

```
go get github.com/cB-Sigma/talq-sim@latest
```

Requires Go 1.22 or later. The command:

```
go install github.com/cB-Sigma/talq-sim/cmd/talqsim@latest
```

## Command line

```
talqsim -listen 127.0.0.1:8095 -token demo-gw-token -walk
```

| Flag | Default | Meaning |
|---|---|---|
| `-listen` | `127.0.0.1:8095` | address to listen on; the gateway API is at `/talq` |
| `-address` | `6f1c0000-0000-4000-8000-00000000d001` | the gateway's TALQ address |
| `-token` | | bearer token the CMS must send (empty: none) |
| `-cms` | | CMS API base to announce the services to |
| `-cms-token` | | bearer token sent to the CMS |
| `-walk` | `false` | run every gateway operation against the gateway once at start, as a self-test |

## In Go tests

```go
g := sim.New("6f1c0000-0000-4000-8000-00000000d001", "")
defer g.Close()
c := &talq.Client{URL: g.URL(), CMSAddress: cmsUUID}
devs, _ := c.ListDevices(ctx, nil, 100)
_, _ = c.Override(ctx, []talq.Ref{{Resource: "devices", Address: sim.Lamp1}}, talq.LevelCommand(40))
// g.Level(sim.Lamp1) == 40
g.SetFault("getDevices", sim.Fault{Status: 429, Key: talq.KeyGenericError})
```

## Documentation

Reference: https://pkg.go.dev/github.com/cB-Sigma/talq-sim

| Package | What it does |
|---|---|
| `sim` | `New`, `NewAt`, `NewTLS`, `Gateway` (state readers such as `Level`, `Device`, `Group`, `Logger`, `Program`, `Calendar`, `AssetType`, `AssignedTo`, `Overrides`, `Served`; fault switches; the CMS side: `Bootstrap`, `Announce`, `AddDevice`, `Event`, `Sample`, `Raw`, `CMSClient`, `CallCMS`), `Walk`, `Step`, `Fault` |
| `cmd/talqsim` | serves the gateway, optionally announces to a CMS and walks every operation |
| `interop` (nested module, tests only) | validates a whole run against the TALQ consortium's OpenAPI schemas |

Changes per version are in [CHANGELOG.md](CHANGELOG.md).

## Testing

```
go test -race ./...
cd interop && SIGMA_TALQ_OAS=fetch go test -v ./...   # downloads the pinned OpenAPI files
```

The tests at the module root drive the talq client against the
simulator: every gateway operation, error answers, outages, delays,
hangs, redirects, paging, 202 answers followed by PATCH `/devices`,
every CMS API operation sent by the simulated gateway to a CMS built
with `talq.Serve`, the bootstrap, and mutual TLS.

### Interop tested against

- Simulator only: no TALQ gateway or CMS of another maker is openly
  available, and certification needs consortium membership. The
  simulator and the talq client are written by the same author from the
  same definitions, so their agreement is not independent.
- Outside evidence: the TALQ consortium's OpenAPI files for 2.7.1
  (github.com/TALQ-consortium/TALQ_specification at commit b05f9a2,
  BSD-3-Clause; SHA-256 checked). The `interop` module runs every
  gateway operation once and validates each request body sent, each
  answer and each error body against the schema the files give for
  that operation and status, and the bodies the simulator sends to a
  CMS against the CMS API's request schemas: 161 and 10 bodies passed
  on 2026-10-11 (santhosh-tekuri/jsonschema v6.0.3, draft 4 rules for
  the OpenAPI 3.0 schemas). The first run found two simulator bugs,
  fixed before the release (see CHANGELOG.md).

## Demo values

Every address, token, device name and asset type in this module is a
made-up demo value.

## Related repositories

- [talq](https://github.com/cB-Sigma/talq): the TALQ client and server
  toolkit this simulator is built on.

## Status

v0.x: the API may change before v1.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

See [SECURITY.md](SECURITY.md).

## Licence

Apache-2.0 (see [LICENSE](LICENSE) and [NOTICE](NOTICE)). Copyright 2026
Abhinav Gautam. TALQ is a trademark of the TALQ Consortium; the name is
used only to say which protocol this module simulates.
