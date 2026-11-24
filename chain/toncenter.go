// Package chain reads balance and seqno and broadcasts an already signed BOC.
// The client has no cookie jar and never sees a mnemonic.
package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBase = "https://toncenter.com/api/v2"
	rpcTimeout  = 10 * time.Second
	bodyLimit   = 1 << 20
)

// Client is the Toncenter v2 broadcaster. Lookup uses the v3 transactionsByMessage method.
type Client struct {
	base   string
	apiKey string
	http   *http.Client
}

// New builds a client. base defaults to https://toncenter.com/api/v2. apiKey may be empty.
func New(base, apiKey string) (*Client, error) {
	if base == "" {
		base = defaultBase
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("chain endpoint must be https")
	}
	return &Client{
		base:   strings.TrimRight(base, "/"),
		apiKey: apiKey,
		http: &http.Client{
			Timeout: rpcTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// SendBOC posts the base64 external message. A lost or rejected response is an error.
// nil means Toncenter accepted the BOC.
func (c *Client) SendBOC(ctx context.Context, boc string) error {
	body, _ := json.Marshal(map[string]string{"boc": boc})
	raw, status, err := c.call(ctx, http.MethodPost, c.base+"/sendBoc", body)
	if err != nil {
		return err
	}
	if status != http.StatusOK || !okField(raw) {
		return fmt.Errorf("toncenter sendBoc status %d", status)
	}
	return nil
}

// Balance returns nanotons. A network failure is an error, not a zero balance.
func (c *Client) Balance(ctx context.Context, rawAddress string) (int64, error) {
	u := c.base + "/getAddressBalance?address=" + url.QueryEscape(rawAddress)
	raw, status, err := c.call(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK || !okField(raw) {
		return 0, fmt.Errorf("toncenter balance status %d", status)
	}
	var probe struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return 0, err
	}
	text := strings.Trim(string(probe.Result), `"`)
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Seqno reads the wallet seqno. A network failure is an error.
func (c *Client) Seqno(ctx context.Context, rawAddress string) (uint32, error) {
	u := c.base + "/getWalletInformation?address=" + url.QueryEscape(rawAddress)
	raw, status, err := c.call(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK || !okField(raw) {
		return 0, fmt.Errorf("toncenter seqno status %d", status)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var probe struct {
		Result struct {
			Seqno json.Number `json:"seqno"`
		} `json:"result"`
	}
	if err := dec.Decode(&probe); err != nil {
		return 0, err
	}
	if probe.Result.Seqno == "" {
		return 0, errors.New("toncenter seqno missing")
	}
	n, err := probe.Result.Seqno.Int64()
	if err != nil || n < 0 || n > int64(^uint32(0)) {
		return 0, errors.New("toncenter seqno")
	}
	return uint32(n), nil
}

// Lookup searches v3 by external message hash.
// An empty HTTP 200 is not found. A transport error or HTTP 5xx is unavailable.
func (c *Client) Lookup(ctx context.Context, msgHash string) (string, error) {
	u := c.v3() + "/transactionsByMessage?msg_hash=" + url.QueryEscape(msgHash) + "&limit=1"
	raw, status, err := c.call(ctx, http.MethodGet, u, nil)
	if err != nil || status >= 500 || status == 0 {
		return "unavailable", err
	}
	if status != http.StatusOK {
		return "unavailable", fmt.Errorf("toncenter lookup status %d", status)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return "unavailable", err
	}
	rawTx, ok := top["transactions"]
	if !ok {
		return "unavailable", errors.New("toncenter lookup has no transactions field")
	}
	var txs []json.RawMessage
	if err := json.Unmarshal(rawTx, &txs); err != nil {
		return "unavailable", err
	}
	if len(txs) == 0 {
		return "not_found", nil
	}
	return "found", nil
}

func (c *Client) v3() string {
	if i := strings.Index(c.base, "/api/v2"); i >= 0 {
		return c.base[:i] + "/api/v3"
	}
	return c.base + "/../api/v3"
}

func (c *Client) call(ctx context.Context, method, rawURL string, body []byte) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(io.LimitReader(res.Body, bodyLimit+1))
	if err != nil {
		return nil, res.StatusCode, err
	}
	if len(buf) > bodyLimit {
		return nil, res.StatusCode, errors.New("toncenter response exceeds 1MiB")
	}
	return buf, res.StatusCode, nil
}

func okField(raw []byte) bool {
	var probe struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.OK
}
