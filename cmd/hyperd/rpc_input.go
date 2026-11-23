package main

import (
	"encoding/json"
	"errors"
	"io"
)

// rpcd has already authorized this method. The session token is transport
// metadata and must never be forwarded to an asynchronous job or its journal.
func rpcInput(r io.Reader) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, (12<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(b) > 12<<20 {
		return nil, errors.New("RPC body too large")
	}
	if len(b) == 0 {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if e = json.Unmarshal(b, &fields); e != nil || fields == nil {
		return nil, errors.New("expected an RPC object")
	}
	if _, ok := fields["ubus_rpc_session"]; !ok {
		return b, nil
	}
	delete(fields, "ubus_rpc_session")
	return json.Marshal(fields)
}
