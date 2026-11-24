package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/xssnick/tonutils-go/address"
)

var (
	ErrAmount     = errors.New("amount")
	ErrInvoice    = errors.New("invoice")
	ErrUnexpected = errors.New("unexpected payment flow")
)

// Message is one Fragment transaction output. AmountNano is already nanotons.
type Message struct {
	Address    string
	AmountNano int64
	Payload    string
	StateInit  string
}

// Invoice is a parsed get*Link transaction. ValidUntil is never the zero time.
type Invoice struct {
	ValidUntil time.Time
	FromRaw    string
	Messages   []Message
}

type linkMessage struct {
	Address   string          `json:"address"`
	Amount    json.RawMessage `json:"amount"`
	Payload   string          `json:"payload"`
	StateInit string          `json:"stateInit"`
}

type linkTx struct {
	ValidUntil  json.RawMessage `json:"validUntil"`
	ValidUntil2 json.RawMessage `json:"valid_until"`
	From        string          `json:"from"`
	Messages    []linkMessage   `json:"messages"`
	EVM         json.RawMessage `json:"evm"`
}

type linkBody struct {
	Error       string          `json:"error"`
	EVM         json.RawMessage `json:"evm"`
	Transaction linkTx          `json:"transaction"`
}

// ParseLink reads a getBuyStarsLink or getGiftPremiumLink body.
// A missing or zero validUntil is an error and must not be stored.
func ParseLink(body []byte) (Invoice, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var raw linkBody
	if err := dec.Decode(&raw); err != nil {
		return Invoice{}, ErrInvoice
	}
	if hasEVM(raw.EVM) || hasEVM(raw.Transaction.EVM) {
		return Invoice{}, ErrUnexpected
	}
	untilRaw := raw.Transaction.ValidUntil
	if len(bytes.TrimSpace(untilRaw)) == 0 || string(untilRaw) == "null" {
		untilRaw = raw.Transaction.ValidUntil2
	}
	until, err := parseUntil(untilRaw)
	if err != nil {
		return Invoice{}, err
	}
	if raw.Transaction.From == "" {
		return Invoice{}, ErrInvoice
	}
	from, err := address.ParseAddr(raw.Transaction.From)
	if err != nil {
		return Invoice{}, ErrInvoice
	}
	if len(raw.Transaction.Messages) == 0 {
		return Invoice{}, ErrInvoice
	}
	msgs := make([]Message, 0, len(raw.Transaction.Messages))
	for _, m := range raw.Transaction.Messages {
		if m.Address == "" {
			return Invoice{}, ErrInvoice
		}
		addr, err := address.ParseAddr(m.Address)
		if err != nil {
			return Invoice{}, ErrInvoice
		}
		amt, err := ParseNanoTON(m.Amount)
		if err != nil || amt <= 0 {
			return Invoice{}, ErrInvoice
		}
		msgs = append(msgs, Message{
			Address:    addr.StringRaw(),
			AmountNano: amt,
			Payload:    m.Payload,
			StateInit:  m.StateInit,
		})
	}
	return Invoice{ValidUntil: until, FromRaw: from.StringRaw(), Messages: msgs}, nil
}

func hasEVM(raw json.RawMessage) bool {
	s := bytes.TrimSpace(raw)
	return len(s) > 0 && string(s) != "null" && string(s) != "false"
}

func parseUntil(raw json.RawMessage) (time.Time, error) {
	s := bytes.TrimSpace(raw)
	if len(s) == 0 || string(s) == "null" {
		return time.Time{}, ErrInvoice
	}
	if s[0] == '"' {
		var text string
		if err := json.Unmarshal(s, &text); err != nil {
			return time.Time{}, ErrInvoice
		}
		if text == "" || text == "0" {
			return time.Time{}, ErrInvoice
		}
		if t, err := time.Parse(time.RFC3339, text); err == nil {
			if t.IsZero() {
				return time.Time{}, ErrInvoice
			}
			return t.UTC(), nil
		}
		n, err := parseInteger(text)
		if err != nil || n <= 0 {
			return time.Time{}, ErrInvoice
		}
		return time.Unix(n, 0).UTC(), nil
	}
	n, err := parseInteger(string(s))
	if err != nil || n <= 0 {
		return time.Time{}, ErrInvoice
	}
	return time.Unix(n, 0).UTC(), nil
}

// ParseTONAmount reads an init amount in TON (scale 9) from a JSON string or number.
// Exponents and more than 9 fractional digits are rejected.
func ParseTONAmount(raw json.RawMessage) (int64, error) {
	text, err := jsonScalar(raw)
	if err != nil {
		return 0, err
	}
	return tonToNano(text)
}

// ParseNanoTON reads a message amount that is already nanotons (scale 0).
// A dot, a comma, or an exponent is rejected.
func ParseNanoTON(raw json.RawMessage) (int64, error) {
	text, err := jsonScalar(raw)
	if err != nil {
		return 0, err
	}
	if strings.ContainsAny(text, "eE.,") {
		return 0, ErrAmount
	}
	return parseInteger(text)
}

func jsonScalar(raw json.RawMessage) (string, error) {
	s := bytes.TrimSpace(raw)
	if len(s) == 0 || string(s) == "null" {
		return "", ErrAmount
	}
	if s[0] == '"' {
		var text string
		if err := json.Unmarshal(s, &text); err != nil {
			return "", ErrAmount
		}
		return strings.TrimSpace(text), nil
	}
	return string(s), nil
}

func tonToNano(text string) (int64, error) {
	if text == "" || strings.ContainsAny(text, "eE,+") || strings.Contains(text, "-") {
		return 0, ErrAmount
	}
	whole, frac, ok := strings.Cut(text, ".")
	if !ok {
		frac = ""
	}
	if whole == "" {
		whole = "0"
	}
	if len(frac) > 9 {
		return 0, ErrAmount
	}
	for len(frac) < 9 {
		frac += "0"
	}
	w, err := parseInteger(whole)
	if err != nil {
		return 0, err
	}
	f, err := parseInteger(frac)
	if err != nil {
		return 0, err
	}
	if w > (1<<62)/1_000_000_000 {
		return 0, ErrAmount
	}
	return w*1_000_000_000 + f, nil
}

func parseInteger(s string) (int64, error) {
	if s == "" {
		return 0, ErrAmount
	}
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, ErrAmount
		}
		next := n*10 + int64(r-'0')
		if next < n {
			return 0, ErrAmount
		}
		n = next
	}
	return n, nil
}

// ErrorText returns a Fragment error string when the body is an API error object.
func ErrorText(body []byte) string {
	var probe struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}
	return strings.TrimSpace(probe.Error)
}

// BadRequest reports whether the error text is exactly "bad request".
func BadRequest(body []byte) bool {
	text := ErrorText(body)
	if text == "" {
		text = strings.TrimSpace(string(body))
	}
	return strings.ToLower(text) == "bad request"
}
