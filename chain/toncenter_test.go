package chain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestToncenter(t *testing.T) {
	var sawCookie bool
	var sawKey string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			sawCookie = true
		}
		sawKey = r.Header.Get("X-API-Key")
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendBoc"):
			body, _ := io.ReadAll(r.Body)
			var probe struct {
				BOC string `json:"boc"`
			}
			_ = json.Unmarshal(body, &probe)
			if probe.BOC != "Ym9j" {
				http.Error(w, "boc", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.Contains(r.URL.Path, "/getAddressBalance"):
			_, _ = w.Write([]byte(`{"ok":true,"result":"500000000"}`))
		case strings.Contains(r.URL.Path, "/getWalletInformation"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"seqno":7}}`))
		case strings.Contains(r.URL.Path, "/transactionsByMessage"):
			if r.URL.Query().Get("msg_hash") == "missing-field" {
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			}
			if r.URL.Query().Get("msg_hash") == "empty" {
				_, _ = w.Write([]byte(`{"transactions":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"transactions":[{"hash":"abc"}]}`))
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	if _, err := New("http://127.0.0.1/api/v2", ""); err == nil {
		t.Fatal("http endpoint accepted")
	}
	c, err := New(srv.URL+"/api/v2", "APIKEYMARKER")
	if err != nil {
		t.Fatal(err)
	}
	if c.http.Jar != nil {
		t.Fatal("cookie jar is set")
	}
	c.http = srv.Client()

	ctx := context.Background()
	if err := c.SendBOC(ctx, "Ym9j"); err != nil {
		t.Fatal(err)
	}
	bal, err := c.Balance(ctx, "0:aa")
	if err != nil || bal != 500_000_000 {
		t.Fatalf("balance %d %v", bal, err)
	}
	seq, err := c.Seqno(ctx, "0:aa")
	if err != nil || seq != 7 {
		t.Fatalf("seqno %d %v", seq, err)
	}
	if got, err := c.Lookup(ctx, "empty"); err != nil || got != "not_found" {
		t.Fatalf("empty %s %v", got, err)
	}
	if got, err := c.Lookup(ctx, "abc"); err != nil || got != "found" {
		t.Fatalf("found %s %v", got, err)
	}
	if got, _ := c.Lookup(ctx, "missing-field"); got != "unavailable" {
		t.Fatalf("missing field %s", got)
	}
	if sawCookie {
		t.Fatal("toncenter saw a cookie")
	}
	if sawKey != "APIKEYMARKER" {
		t.Fatalf("api key %q", sawKey)
	}
}
