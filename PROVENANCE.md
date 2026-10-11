# Provenance

## How the code was written

Written for SigmaM2M, a oneM2M platform, in Go, by the author of the
talq module and from the same TALQ 2.7.1 OpenAPI definitions
(github.com/TALQ-consortium/TALQ_specification, commit
b05f9a2c8df945fceab5f47c0ebc045724dc77e0). No code was ported or copied
from another implementation; none is open.

The simulator was first written next to SigmaM2M's TALQ adapter to test
it without a gateway, moved into the talq module on 2026-10-06 (with
`git mv`, so history follows sim/sim.go and sim/ext.go) and out of it
into this module on 2026-10-11, so the library does not depend on its
simulator.

For the first release it was extended to answer every operation of the
gateway API (sim/more.go), routed by talq's operation table, and given
the fault switches, the CMS side and `Walk` (sim/walk.go). An
independent review, made from the public definitions only, listed the
faults a simulator should produce (the error keys and statuses of the
definitions, 202 answers followed by a PATCH to the CMS, timeouts,
redirects, client certificate refusals); those are what the switches
cover.

The `interop` module then validated a whole run against the OpenAPI
schemas. It found two simulator bugs, fixed the same day: the logger
checks accepted configurations without `sourceEvents` (event recording)
or `delay` (immediate reporting), which the schemas require, and the
bootstrap's PATCH of the gateway's own device lacked `name` and
`class`.

How the gateway behaves where the definitions leave it open (a release
override going back to level 100, the order of list answers, which
services it announces, the stored log report per logger) is our own
choice.

## Sources

| Area (files) | Followed |
|---|---|
| Simulator (sim/) | TALQ 2.7.1 gateway, CMS and data model OpenAPI files (paths, statuses, required properties, error keys), through the talq module |
| Command (cmd/talqsim) | none (flags are our own) |
| Interop (interop/) | the same OpenAPI files, read at test time |

No specification text is reproduced.

## Tests

| Test | Source |
|---|---|
| coverage_test.go, cmd/talqsim | the talq client against this simulator, written from the same definitions; not a real gateway |
| interop/ | a run of every operation validated against the consortium's own schemas |

All addresses, tokens and names are made-up demo values.
