# agy-provider

The helper behind `agy provider`. It manages a registry of custom endpoints and,
when the engine cannot speak to one of them itself, runs a loopback proxy that
translates for it.

Pure Go, standard library only, no cgo — so it cross-compiles to
`android/arm64` in one command and stays as relocatable as the rest of the
package. The single binary is both the command line and the proxy.

## Why a proxy at all

The engine has exactly one seam for this: `modelProvider: "gemini"` in
`settings.json` makes it read `GEMINI_API_KEY` and `GOOGLE_GEMINI_BASE_URL` from
the environment and talk to that host in Gemini's dialect. The bootstrapper fills
those in before handing off (`apply_provider_environment` in
`lib/agy_helper.c`), which is enough on its own for a Gemini-shaped host with one
key.

It is not enough for anything else. An OpenAI- or Anthropic-shaped host speaks a
different dialect, and rotating between several keys means choosing one *per
request*, which cannot be done through a variable set once at launch. So those
cases go through the proxy, and Gemini's shape is the pivot every translation is
written against.

## Layout

| Package | What it owns |
| --- | --- |
| `internal/config` | the registry file, URL joining per dialect, validation |
| `internal/state` | rotation cursor, key health, the running proxy; flushed on a timer |
| `internal/keys` | which key a request goes out with, and what a rejection costs it |
| `internal/wire` | the three dialects and the translation between them |
| `internal/sse` | reading and writing event streams |
| `internal/proxy` | the loopback server: routing, auth, the attempt loop, relaying |
| `internal/discover` | asking a host what it serves, and whether a key works |
| `internal/settings` | the one key this touches in the CLI's own settings.json |
| `internal/launch` | what `up` does: decide, start a proxy, produce the environment |
| `internal/termuxnet` | the HTTP client, and DNS on a device with no /etc/resolv.conf |
| `cmd/agy-provider` | the subcommands |

## Things worth knowing before changing it

- **Gemini pairs a tool result with its call by name; the other two use an id the
  model issued.** The transcript arrives whole on every request, so `callIDs` in
  `wire/bridge.go` walks it in order and issues ids as calls appear. Change that
  ordering and multi-call turns start answering the wrong call.
- **Anthropic will reject a request that Gemini's transcript makes natural.**
  Roles must alternate (two Gemini `user` turns in a row merge), the first turn
  must be the user's, `max_tokens` is required, `tool_result` blocks must lead
  their turn, and an explicit temperature is refused while thinking is enabled.
  All of that is in `ToAnthropic`, and all of it is what the tests pin down.
- **Tool schemas arrive as an OpenAPI subset with proto enum names**, so
  `"type":"STRING"` has to be lowercased and `nullable` dropped before either
  other host will accept it (`normalizeSchema`).
- **A retry is only invisible before the first byte.** The attempt loop writes
  nothing to the client until a host has accepted the request. Once a stream has
  started, a failure is delivered as the last thing the model "said" rather than
  as a stream that stops silently.
- **The launcher must never fail a launch.** `launch.Up` returns notes instead of
  errors; a broken registry means the engine starts on its own sign-in.
- **The proxy inherits its listener.** `EnsureProxy` binds the port, hands the fd
  to the child as fd 3 and starts it with `setsid`, so the port is already
  accepting by the time the environment is printed and the engine cannot arrive
  too early.
- **`android/arm64`, not `linux/arm64`.** Android refuses a non-PIE executable,
  and only the android target is PIE by default.
- **No cgo means Go's own resolver**, which reads `/etc/resolv.conf` — a file
  Android does not have. `termuxnet` reads `$PREFIX/etc/resolv.conf` instead. This
  is the same problem the bootstrapper solves for the engine with
  `GODEBUG=netdns=cgo`.

## Checks

```bash
cd provider
gofmt -l .            # must print nothing
go vet ./...
go test -race ./...
./scripts/smoke.sh    # end to end against a stub host, including the detached proxy
```

`scripts/smoke.sh` covers what the unit tests cannot reach: the detached proxy and
its inherited listener, the launch environment, rotation past a rejected key, and
a streamed reply from end to end. The unit tests assert against real HTTP — every
dialect test runs against a loopback stand-in rather than a mock, so what is
checked is the bytes that actually go out.
