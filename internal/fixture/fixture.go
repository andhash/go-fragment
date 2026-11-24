// Package fixture replays a synthetic Fragment session in memory.
// It does not dial. Cookie headers are removed from the recorded calls.
package fixture

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/xssnick/tonutils-go/address"
)

// Step is one scripted Fragment method.
type Step struct {
	Method string          `json:"method"`
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
	Drop   bool            `json:"drop"`
}

// Script is schema 1 of a synthetic purchase.
type Script struct {
	Schema int    `json:"schema"`
	Steps  []Step `json:"steps"`
}

// Call is one HTTP request after Sanitize.
type Call struct {
	Method     string
	URL        string
	FormMethod string
	Form       url.Values
	Header     http.Header
	Body       string
}

// Player is an HTTPDoer. GET returns the product page. POST consumes a step.
type Player struct {
	mu       sync.Mutex
	html     []byte
	steps    []Step
	fallback map[string]Step
	calls    []Call
	sawCook  bool
}

// New replays steps. html is the product page served for every GET.
func New(html []byte, steps []Step) *Player {
	cp := append([]Step(nil), steps...)
	return &Player{html: append([]byte(nil), html...), steps: cp, fallback: map[string]Step{}}
}

// Load reads the product page and a schema-1 script.
func Load(htmlPath, scriptPath string) (*Player, error) {
	html, err := os.ReadFile(htmlPath)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		return nil, err
	}
	var script Script
	if err := json.Unmarshal(raw, &script); err != nil {
		return nil, err
	}
	if script.Schema != 1 {
		return nil, os.ErrInvalid
	}
	return &Player{html: html, steps: script.Steps, fallback: map[string]Step{}}, nil
}

// Insert queues a step ahead of the ones loaded from disk.
func (p *Player) Insert(step Step) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.steps = append([]Step{step}, p.steps...)
}

// Fallback is served when a method has no queued step left.
func (p *Player) Fallback(method string, status int, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fallback == nil {
		p.fallback = map[string]Step{}
	}
	p.fallback[method] = Step{Method: method, Status: status, Body: json.RawMessage(body)}
}

// Calls returns sanitized requests. Cookie is not included.
func (p *Player) Calls() []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Call, len(p.calls))
	copy(out, p.calls)
	return out
}

// SawCookie reports whether any request carried a Cookie header.
func (p *Player) SawCookie() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sawCook
}

// Methods lists form methods in post order.
func (p *Player) Methods() []string {
	var out []string
	for _, c := range p.Calls() {
		if c.FormMethod != "" {
			out = append(out, c.FormMethod)
		}
	}
	return out
}

// Do implements fragment.HTTPDoer. It never dials.
func (p *Player) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	var raw []byte
	if req.Body != nil {
		raw, _ = io.ReadAll(req.Body)
	}
	form, _ := url.ParseQuery(string(raw))
	if req.Header.Get("Cookie") != "" {
		p.mu.Lock()
		p.sawCook = true
		p.mu.Unlock()
	}
	call := Call{
		Method:     req.Method,
		URL:        req.URL.String(),
		FormMethod: form.Get("method"),
		Form:       form,
		Header:     Sanitize(req.Header),
		Body:       string(raw),
	}
	p.mu.Lock()
	p.calls = append(p.calls, call)
	p.mu.Unlock()

	if req.Method == http.MethodGet {
		return response(http.StatusOK, p.html), nil
	}
	step, ok := p.take(form.Get("method"))
	if !ok {
		return response(http.StatusInternalServerError, []byte(`{"error":"no fixture"}`)), nil
	}
	if step.Drop {
		return nil, io.ErrUnexpectedEOF
	}
	status := step.Status
	if status == 0 {
		status = http.StatusOK
	}
	body := bytes.ReplaceAll(step.Body, []byte("@account"), []byte(friendly(form.Get("account"))))
	return response(status, body), nil
}

func (p *Player) take(method string) (Step, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, step := range p.steps {
		if step.Method == method {
			p.steps = append(p.steps[:i], p.steps[i+1:]...)
			return step, true
		}
	}
	step, ok := p.fallback[method]
	return step, ok
}

func friendly(accountJSON string) string {
	var probe struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal([]byte(accountJSON), &probe); err != nil || probe.Address == "" {
		return "@account"
	}
	addr, err := address.ParseRawAddr(probe.Address)
	if err != nil {
		addr, err = address.ParseAddr(probe.Address)
	}
	if err != nil {
		return "@account"
	}
	addr.SetBounce(true)
	return addr.String()
}

func response(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Status:        http.StatusText(status),
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

// Sanitize copies headers and drops cookie and authorization.
func Sanitize(h http.Header) http.Header {
	out := http.Header{}
	for k, vs := range h {
		if strings.EqualFold(k, "Cookie") || strings.EqualFold(k, "Authorization") {
			continue
		}
		out[k] = append([]string(nil), vs...)
	}
	return out
}
