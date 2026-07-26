# Architecture

WebGuard Instance is a worker that obtains monitoring work from WebGuard Core,
executes network checks, and reports normalized results. The current design is
an incremental clean-architecture migration: package boundaries are explicit
without changing the existing Core API contract.

## Dependency rule

Dependencies point towards application policy. The application package cannot
import HTTP, DNS, TLS, RDAP, WHOIS, process-execution, or server packages.

```text
cmd/webguard-instance              composition root
        |
internal/application               run lifecycle and ports
        |
internal/domain/monitor            monitoring and result types
        ^
internal/adapters/*                Core API, health, scheduling, and network transports
```

`cmd/webguard-instance` is the only composition root. It reads configuration,
creates concrete adapters, and passes them through the application ports.

## Application layer

`application.Coordinator` executes independent monitoring phases and reports
phase failures. It only depends on the `Phase` and `MonitoringGateway` ports.
It preserves the current best-effort run result while the bounded execution
controller introduces richer result and retry semantics.

## Domain layer

`domain/monitor` owns the Core-compatible monitoring input, monitor types,
statuses, and result payloads. These types do not depend on transports.

## Adapters

- `adapters/coreapi` implements the WebGuard Core HTTP client.
- `adapters/health` provides liveness HTTP handlers and graceful server
  shutdown.
- `adapters/runner` registers independent HTTP, keyword, ping, TCP, DNS, TLS,
  and domain-expiry executors. Each executor produces normalized result
  payloads, which the shared publisher sends to Core.
- `adapters/domainlookup` and `adapters/target` isolate RDAP/WHOIS and
  egress-policy details.

The next migration step is a bounded execution controller that applies global
and per-executor concurrency limits without changing the Core API contract.
