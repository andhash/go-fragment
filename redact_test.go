package fragment

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactionMarkers(t *testing.T) {
	RegisterSecrets("COOKIEMARKER", "DTMARKER", "SEEDMARKER", "PUBKEYMARKER", "PROOFMARKER", "APIKEYMARKER")
	var buf bytes.Buffer
	log := slog.New(RedactingHandler{Next: slog.NewTextHandler(&buf, nil)})
	log.Info("cookie COOKIEMARKER dt DTMARKER",
		slog.String("cookie", "COOKIEMARKER"),
		slog.String("stel_dt", "DTMARKER"),
		slog.String("seed", "SEEDMARKER"),
		slog.String("publicKey", "PUBKEYMARKER"),
		slog.String("proof", "PROOFMARKER"),
		slog.String("api_key", "APIKEYMARKER"),
	)
	log.Info("https://toncenter.com/api/v2/jsonRPC?api_key=APIKEYMARKER")
	got := buf.String()
	for _, marker := range []string{"COOKIEMARKER", "DTMARKER", "SEEDMARKER", "PUBKEYMARKER", "PROOFMARKER", "APIKEYMARKER"} {
		if strings.Contains(got, marker) {
			t.Fatalf("log kept %s: %s", marker, got)
		}
	}
}
