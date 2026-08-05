# Smoke tests

Run against a real, already-built `docker compose` stack (or let
`clean-startup.sh` build it). Each script is `set -eu`: it exits non-zero on
the first failed check, with a message explaining what failed.

| Script | Proves |
| --- | --- |
| [`clean-startup.sh`](clean-startup.sh) | `docker compose up --build` on a machine with no prior state (no volumes) reaches full health with zero manual steps -- including that `ledger-migrate`/`consolidation-migrate` actually ran and exited 0, which is what makes every other service's Postgres auth work at all (see T10 session notes / `docs/compliance-matrix.md` CH-09). |
| [`restart-isolation.sh`](restart-isolation.sh) | Stopping every Consolidation-exclusive container does not affect the Ledger's readiness or its ability to accept a request (T10 Aceite). |
| [`replica-loss.sh`](replica-loss.sh) | Killing and restarting a single stateless service instance recovers cleanly without corrupting or degrading the rest of the stack. This Compose stack has no multi-replica services (that is Swarm-only, out of scope here -- see [`docs/runbooks/replica-loss.md`](../../docs/runbooks/replica-loss.md)), so this proves the underlying statelessness property real replica loss depends on, not multi-replica failover itself. |

## Not implemented: cache fallback

The fourth smoke test the T10 ticket names is cache-fallback-on-Redis-outage. There is no cache/Redis in this system (T07 did not build one -- see [`docs/runbooks/redis.md`](../../docs/runbooks/redis.md) and the Grafana dashboard's Cache panel). A script here would have nothing to test against; it is not stubbed out with a fake pass.

## Running

```sh
./scripts/smoke/clean-startup.sh       # also builds/starts the stack
./scripts/smoke/restart-isolation.sh   # requires the stack already up
./scripts/smoke/replica-loss.sh        # requires the stack already up
```

Or all three via `make smoke`.
