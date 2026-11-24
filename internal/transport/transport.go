// Package transport posts form requests to fragment.com and nowhere else.
package transport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/andhash/go-fragment/internal/protocol"
)

const (
	bodyLimit = 1 << 20
	hashTTL   = 120 * time.Second
)

// ErrHashNotFound means the product page has no api hash.
var ErrHashNotFound = errors.New("hash not found")

// Cookies are sent only to https://fragment.com.
type Cookies struct {
	SSID, DT, Token, TONToken string
}

// Config builds the default Transport.
type Config struct {
	Cookies Cookies
	Doer    interface {
		Do(ctx context.Context, req *http.Request) (*http.Response, error)
	}
	Now func() time.Time
}

// Client is the default Fragment transport. Redirects are disabled.
type Client struct {
	cookies Cookies
	doer    Config
	now     func() time.Time
	mu      sync.Mutex
	hash    map[string]hashEntry
}

type hashEntry struct {
	value string
	at    time.Time
}

type doerFunc func(ctx context.Context, req *http.Request) (*http.Response, error)

func (f doerFunc) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	return f(ctx, req)
}

// New returns a transport. A nil doer uses net/http with redirects disabled.
func New(cfg Config) (*Client, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Doer == nil {
		httpClient := &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		cfg.Doer = doerFunc(func(ctx context.Context, req *http.Request) (*http.Response, error) {
			return httpClient.Do(req.WithContext(ctx))
		})
	}
	return &Client{cookies: cfg.Cookies, doer: cfg, now: cfg.Now, hash: map[string]hashEntry{}}, nil
}

// Call posts method and fields to fragment.com. One bad-request response refreshes the hash and retries once.
type Call struct {
	Page   string
	Method string
	Fields url.Values
}

// Reply is the HTTP status and the body.
type Reply struct {
	HTTPStatus int
	Body       []byte
}

func (c *Client) Call(ctx context.Context, call Call) (Reply, error) {
	reply, err := c.post(ctx, call)
	if err != nil {
		return Reply{}, err
	}
	if protocol.BadRequest(reply.Body) {
		c.dropHash(call.Page)
		return c.post(ctx, call)
	}
	return reply, nil
}

func (c *Client) post(ctx context.Context, call Call) (Reply, error) {
	if err := allow(call.Page); err != nil {
		return Reply{}, err
	}
	hash, err := c.pageHash(ctx, call.Page)
	if err != nil {
		return Reply{}, err
	}
	endpoint := "https://fragment.com/api?hash=" + hash
	fields := url.Values{}
	for k, vs := range call.Fields {
		fields[k] = append([]string(nil), vs...)
	}
	fields.Set("method", call.Method)
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(fields.Encode()))
	if err != nil {
		return Reply{}, err
	}
	setHeaders(req, call.Page)
	req.Header.Set("Cookie", cookieHeader(c.cookies))
	return c.do(ctx, req)
}

func (c *Client) pageHash(ctx context.Context, page string) (string, error) {
	c.mu.Lock()
	if e, ok := c.hash[page]; ok && c.now().Sub(e.at) < hashTTL && e.value != "" {
		c.mu.Unlock()
		return e.value, nil
	}
	c.mu.Unlock()
	req, err := http.NewRequest(http.MethodGet, page, nil)
	if err != nil {
		return "", err
	}
	setHeaders(req, page)
	req.Header.Set("Cookie", cookieHeader(c.cookies))
	reply, err := c.do(ctx, req)
	if err != nil {
		return "", err
	}
	if reply.HTTPStatus < 200 || reply.HTTPStatus >= 300 {
		return "", ErrHashNotFound
	}
	hash, ok := protocol.HashFromHTML(reply.Body)
	if !ok {
		return "", ErrHashNotFound
	}
	c.mu.Lock()
	c.hash[page] = hashEntry{value: hash, at: c.now()}
	c.mu.Unlock()
	return hash, nil
}

func (c *Client) dropHash(page string) {
	c.mu.Lock()
	delete(c.hash, page)
	c.mu.Unlock()
}

func (c *Client) do(ctx context.Context, req *http.Request) (Reply, error) {
	if err := allow(req.URL.String()); err != nil {
		return Reply{}, err
	}
	res, err := c.doer.Doer.Do(ctx, req)
	if err != nil {
		return Reply{}, err
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(io.LimitReader(res.Body, bodyLimit+1))
	if err != nil {
		return Reply{}, err
	}
	if len(buf) > bodyLimit {
		return Reply{}, errors.New("fragment response exceeds 1MiB")
	}
	return Reply{HTTPStatus: res.StatusCode, Body: buf}, nil
}

func allow(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" || u.Host != "fragment.com" && u.Host != "fragment.com:443" {
		return errors.New("fragment transport refuses " + u.Host)
	}
	if u.User != nil {
		return errors.New("fragment transport refuses userinfo")
	}
	return nil
}

func setHeaders(req *http.Request, page string) {
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	if req.Method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	}
	req.Header.Set("Origin", "https://fragment.com")
	req.Header.Set("Referer", page)
	req.Header.Set("X-Aj-Referer", page)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
}

func cookieHeader(c Cookies) string {
	parts := make([]string, 0, 4)
	if c.SSID != "" {
		parts = append(parts, "stel_ssid="+c.SSID)
	}
	if c.DT != "" {
		parts = append(parts, "stel_dt="+c.DT)
	}
	if c.Token != "" {
		parts = append(parts, "stel_token="+c.Token)
	}
	if c.TONToken != "" {
		parts = append(parts, "stel_ton_token="+c.TONToken)
	}
	return strings.Join(parts, "; ")
}
