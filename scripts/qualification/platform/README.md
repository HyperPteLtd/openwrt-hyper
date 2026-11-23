# Platform subscription qualification

## Account and external-link imports

Run the client against a disposable HTTPS service on loopback. Create a test member with one active plan and synthetic nodes. In a private fixture directory, place the service's test CA in `cert.pem`, the synthetic login fields `email` and `password` in `member.json`, and the optional anonymous subscription URL in `share-url`. Keep the directory at mode 0700. Subscription bodies and credentials remain local to the fixture.

```sh
HYPER_DIAGNOSTIC_CORE=/path/to/mihomo go run ./scripts/qualification/platform/ingest "$TEST_SERVICE_PORT" "$NEW_PRIVATE_DIR"
```

Set the test service to `both` for the default command. For a service configured as `app` or `link`, append `--app-only` or `--link-only` respectively. The harness checks the mode in the authenticated plan response and counts which download endpoint the router calls. The router logs in, saves the account, refreshes the plan and reloads its saved profile. With a share URL it also checks the anonymous entry point, including the absence of account credentials on those requests. The harness compares the imported nodes, group members, providers, rule order and final policy to the served YAML. It compiles each imported snapshot and runs the pinned binary with `-t` when `HYPER_DIAGNOSTIC_CORE` is set. Set `HYPER_TEST_NAIVE=1` only when the candidate advertises Cronet. The synthetic 16-protocol inventory produces 15 native Mihomo outbounds because the upstream template excludes MTProxy.

## Check content negotiation on an external subscription

Pass a **file containing the URL**, with mode 0600, and a private test directory. The command uses the router’s configured preset UAs: `clash.meta`, `sing-box`, `clash` and `Hyper`. To run a particular comparison, append those UA strings (or other public client UAs) as additional positional arguments. It downloads the subscription and any referenced rule providers. It never starts the core or dials the returned nodes. Production filtering, size limits, validation and last-good retention all run in a new disposable source store. The store is removed on completion. Treat your original URL file as a credential.

```sh
install -d -m 700 "$PRIVATE_TEST_DIR"
go run ./scripts/qualification/platform/external \
  "$PRIVATE_TEST_DIR/subscription-url" "$PRIVATE_TEST_DIR"
```

Output contains the primary HTTP status and format, accepted-node counts, notice codes, update decisions, and whether the previous saved snapshot stayed identical. A rule-provider download failure is reported independently of success downloading the primary template. The URL, server addresses, node names, keys and exported bodies are omitted from this output.
