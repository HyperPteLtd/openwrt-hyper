package subscription

import (
	"net/http"
	"testing"
)

func TestSubscriptionTitle(t *testing.T) {
	for _, c := range []struct {
		h    http.Header
		want string
	}{
		{http.Header{"Profile-Title": {"机场 A"}}, "机场 A"},
		{http.Header{"Profile-Title": {"base64:5py65Zy6IEI="}}, "机场 B"},
		{http.Header{"Content-Disposition": {"attachment; filename*=UTF-8''%E6%9C%BA%E5%9C%BA.yaml"}}, "机场"},
		{http.Header{"Profile-Title": {"https://x/?token=1"}}, ""},
		{http.Header{}, ""},
	} {
		if got := subscriptionTitle(c.h); got != c.want {
			t.Errorf("%v: got %q want %q", c.h, got, c.want)
		}
	}
}

func TestUnnamedLinkUsesHost(t *testing.T) {
	s := OpenStore(t.TempDir())
	if err := s.Configure(Source{ID: "src-a", Kind: "external", URL: "https://sub.example.com/x?token=1"}); err != nil {
		t.Fatal(err)
	}
	r, err := s.read("src-a")
	if err != nil || r.Config.Name != "sub.example.com" || !r.Config.AutoName {
		t.Fatalf("%+v %v", r.Config, err)
	}
	if err := s.Configure(Source{ID: "src-b", Kind: "external", Name: "Mine", URL: "https://sub.example.com/"}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.read("src-b"); r.Config.AutoName {
		t.Fatal("a typed name is kept")
	}
}
