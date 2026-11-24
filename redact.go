package fragment

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
)

const redacted = "redacted"

var secretKeys = map[string]struct{}{
	"cookie": {}, "cookies": {}, "mnemonic": {}, "seed": {}, "boc": {},
	"payload": {}, "authorization": {}, "stel_ssid": {}, "stel_dt": {},
	"stel_token": {}, "stel_ton_token": {}, "signature": {}, "private_key": {},
	"public_key": {}, "publickey": {}, "api_key": {}, "x-api-key": {},
	"ton_proof": {}, "proof": {},
}

var apiKeyQuery = regexp.MustCompile(`(?i)(api_key=)[^&\s]+`)

var (
	secretMu sync.Mutex
	secrets  []string
)

// RegisterSecrets remembers values that Scrub must remove from error text.
// Empty strings are ignored. The logger still drops secret attribute keys
// even when the value was not registered.
func RegisterSecrets(values ...string) {
	secretMu.Lock()
	defer secretMu.Unlock()
	for _, v := range values {
		if len(v) < 4 {
			continue
		}
		secrets = append(secrets, v)
	}
}

// Scrub removes registered secret values and query api_key values.
func Scrub(s string) string {
	if s == "" {
		return s
	}
	s = apiKeyQuery.ReplaceAllString(s, "${1}"+redacted)
	secretMu.Lock()
	defer secretMu.Unlock()
	for _, sec := range secrets {
		if sec != "" && strings.Contains(s, sec) {
			s = strings.ReplaceAll(s, sec, redacted)
		}
	}
	return s
}

func redactedLen(s string) string {
	return redacted + " len=" + itoa(len(s))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// RedactingHandler drops secret attribute keys and scrubs the message.
type RedactingHandler struct {
	Next slog.Handler
}

func (h RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if h.Next == nil {
		return false
	}
	return h.Next.Enabled(ctx, level)
}

func (h RedactingHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.Next == nil {
		return nil
	}
	r.Message = Scrub(r.Message)
	filtered := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		filtered = append(filtered, scrubAttr(a))
		return true
	})
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	for _, a := range filtered {
		out.AddAttrs(a)
	}
	return h.Next.Handle(ctx, out)
}

func (h RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = scrubAttr(a)
	}
	next := h.Next
	if next != nil {
		next = next.WithAttrs(clean)
	}
	return RedactingHandler{Next: next}
}

func (h RedactingHandler) WithGroup(name string) slog.Handler {
	next := h.Next
	if next != nil {
		next = next.WithGroup(name)
	}
	return RedactingHandler{Next: next}
}

func scrubAttr(a slog.Attr) slog.Attr {
	key := strings.ToLower(a.Key)
	if _, drop := secretKeys[key]; drop {
		return slog.String(a.Key, redacted)
	}
	if a.Value.Kind() == slog.KindString {
		return slog.String(a.Key, Scrub(a.Value.String()))
	}
	if a.Value.Kind() == slog.KindGroup {
		gs := a.Value.Group()
		for i := range gs {
			gs[i] = scrubAttr(gs[i])
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(gs...)}
	}
	return a
}
