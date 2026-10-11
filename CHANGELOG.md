# Changelog

All notable changes to this module. Versions follow semantic versioning;
before v1 the API may change in minor versions.

## v0.1.0 (2026-10-11)

First release as a module of its own, split from the talq module: the
simulated gateway (`sim`) and `cmd/talqsim`.

- Every operation of the TALQ 2.7.1 gateway API is answered, routed by
  talq's operation table: services, device classes, counts, device
  add, replace, PATCH and delete, functions and attribute PATCHes, bulk
  updates of every collection, the DALI types, group member counts,
  pulled log reports, assign command lists, data packages.
- Requests are checked with `talq.CheckRequest`; error answers carry
  the TALQ recommended keys (the earlier simulator's own keys are now
  their descriptions).
- Faults: `SetFault`, `SetDelay`, `SetHang`, `SetAccepted` (202, then
  PATCH `/devices` to the CMS), `SetMaxCalendars`,
  `SetStrictReferences`, `SetRedirectAll` (301, 302, 307, 308 on any
  method), `SetCountText`, `SetIgnorePaging`, `SetBootstrapping` (403);
  `NewTLS` for client certificates.
- Towards a CMS: `Bootstrap`, `CMSClient`, `CallCMS`.
- `Walk` runs every gateway operation; `talqsim -walk` does it at start.
- Fixed after validating against the OpenAPI schemas (`interop`): the
  logger checks now require `sourceEvents` for event recording and
  `delay` for immediate reporting, as the schemas do, and the
  bootstrap's PATCH of the gateway's own device carries its name and
  class.
- The client tests that drive the simulator moved here from talq; new
  tests for every operation, faults, paging, 202 answers, the CMS API
  and mutual TLS; a nested test-only module, `interop`, validating a
  run against the TALQ OpenAPI schemas.
