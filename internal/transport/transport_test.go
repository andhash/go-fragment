package transport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/andhash/go-fragment/internal/fixture"
	"github.com/andhash/go-fragment/internal/protocol"
)

func TestHeadersCookieAndHashRefresh(t *testing.T) {
	html := []byte(`<html>/api?hash=0123456789abcdef</html>`)
	var n int
	doer := doerFunc(func(_ context.Context, req *http.Request) (*http.Response, error) {
		n++
		if req.URL.Host != "fragment.com" {
			t.Errorf("host %s", req.URL.Host)
		}
		if req.Header.Get("Cookie") == "" {
			t.Error("cookie missing on fragment.com")
		}
		if got := fixture.Sanitize(req.Header).Get("Cookie"); got != "" {
			t.Errorf("sanitized cookie %q", got)
		}
		if req.Header.Get("Sec-Fetch-Dest") != "empty" || req.Header.Get("Sec-Fetch-Mode") != "cors" || req.Header.Get("Sec-Fetch-Site") != "same-origin" {
			t.Errorf("sec-fetch %+v", req.Header)
		}
		if req.Header.Get("Sec-Fetch-User") != "" || req.Header.Get("Upgrade-Insecure-Requests") != "" {
			t.Error("browser-only headers were set")
		}
		if req.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Error("x-requested-with")
		}
		body := []byte(`{"ok":true}`)
		if req.Method == http.MethodGet {
			body = html
		}
		if req.Method == http.MethodPost && n < 4 {
			body = []byte(`{"error":"Bad Request"}`)
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})
	c, err := New(Config{Cookies: Cookies{SSID: "COOKIEMARKER", DT: "DTMARKER", Token: "t", TONToken: "ton"}, Doer: doer})
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string][]string{"lv": {"false"}}
	// url.Values literal
	reply, err := c.Call(context.Background(), Call{
		Page:   protocol.Stars.Page,
		Method: protocol.Stars.StateMethod,
		Fields: fields,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(reply.Body, []byte(`"ok":true`)) {
		t.Fatalf("body %s", reply.Body)
	}
	if !strings.Contains(protocol.Stars.Page, "fragment.com/stars/buy") {
		t.Fatal(protocol.Stars.Page)
	}
}

type countingDoer struct {
	fn func(context.Context, *http.Request) (*http.Response, error)
}

func (d countingDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	return d.fn(ctx, req)
}

func TestRefusesOtherHostAndHugeBody(t *testing.T) {
	called := 0
	c, err := New(Config{Doer: countingDoer{fn: func(context.Context, *http.Request) (*http.Response, error) {
		called++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("a"), bodyLimit+1))), Header: make(http.Header)}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(context.Background(), Call{Page: "https://evil.example/stars/buy", Method: "x"}); err == nil {
		t.Fatal("evil host accepted")
	}
	if called != 0 {
		t.Fatal("doer was called for a foreign host")
	}
	c.hash["https://fragment.com/stars/buy"] = hashEntry{value: "0123456789abcdef"}
	if _, err := c.Call(context.Background(), Call{Page: "https://fragment.com/stars/buy", Method: "x"}); err == nil {
		t.Fatal("oversize body accepted")
	}
}
