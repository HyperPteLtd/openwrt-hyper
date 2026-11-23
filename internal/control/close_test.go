package control

import (
	"context"
	"net/http"
	"slices"
	"testing"
)

func TestCloseThroughClosesOnlyThatGroupsConnections(t *testing.T) {
	var closed []string
	client := serveController(t, func(r *http.Request) (int, string) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/connections":
			return 200, `{"connections":[{"id":"a","chains":["node:1","@global"]},{"id":"b","chains":["DIRECT"]},{"id":"c","chains":["node:2","group:video","@global"]}]}`
		case r.Method == "DELETE":
			closed = append(closed, r.URL.Path)
			return 204, ""
		}
		t.Fatal(r.Method, r.URL)
		return 0, ""
	})
	if e := client.CloseThrough(context.Background(), "@global"); e != nil || !slices.Equal(closed, []string{"/connections/a", "/connections/c"}) {
		t.Fatal(e, closed)
	}
}
