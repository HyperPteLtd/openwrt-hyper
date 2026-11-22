# Golden responses

`rpc/*.json` are responses of the read-only `luci.hyper` methods with stubbed ubus/uci, first
recorded from the previous implementation and updated to the current settings (`get_config` has no
IPv6 / fake-ip switches; `status.core_version`). The web UI depends on these shapes;
`cmd/hyperd/rpc_test.go` checks hyperd returns the same keys and types.
`nodes.json` lists AnyTLS nodes only, the one type the Hyper service sends (ids, labels and countries
as recorded).

Methods that mutate state, reach the network or need a running core (`select_node`, `latency`,
`traffic`, `diag`, `health_check`, `login`, `checkin`, `check_update`, ...) were not recorded; the
hyperd tests, the end-to-end test and the on-router test cover them.
