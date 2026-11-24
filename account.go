package fragment

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// Cookies is the caller's Fragment session. Values are redacted by fmt and slog.
type Cookies struct {
	SSID     string `json:"stel_ssid"`      // stel_ssid
	DT       string `json:"stel_dt"`        // stel_dt
	Token    string `json:"stel_token"`     // stel_token
	TONToken string `json:"stel_ton_token"` // stel_ton_token
}

func (c Cookies) String() string   { return redactedLen(c.SSID + c.DT + c.Token + c.TONToken) }
func (c Cookies) GoString() string { return c.String() }
func (c Cookies) LogValue() slog.Value {
	return slog.StringValue(c.String())
}

func (c Cookies) ready() bool {
	return c.SSID != "" && c.DT != "" && c.Token != ""
}

func (c Cookies) linkReady() bool {
	return c.ready() && c.TONToken != ""
}

// WalletVersion selects the local contract. v1 accepts V4R2 and V5R1 only.
type WalletVersion string

const (
	WalletV4R2 WalletVersion = "v4r2"
	WalletV5R1 WalletVersion = "v5r1"
)

// Account is the TON Connect identity posted to Fragment. PublicKey is not a log field.
type Account struct {
	Address         string // raw workchain:hex
	Chain           string // "-239" on mainnet
	WalletStateInit string // base64 BOC
	PublicKey       string // hex
}

// HTTPDoer performs one HTTP request. The context is an argument, not only req.Context.
type HTTPDoer interface {
	Do(ctx context.Context, req *http.Request) (*http.Response, error)
}

// Call is one Fragment form POST. Page is the product URL used as referer.
type Call struct {
	Page   string
	Method string
	Fields url.Values
}

// Reply is a Fragment HTTP response. Body is capped at 1 MiB by the default transport.
type Reply struct {
	HTTPStatus int
	Body       json.RawMessage
}

// Transport talks only to fragment.com. A test can replace it without importing internal.
type Transport interface {
	Call(ctx context.Context, call Call) (Reply, error)
}

// OutgoingMessage is one wallet output. AmountNano is nanotons, scale 0.
type OutgoingMessage struct {
	Address    string `json:"address"`
	AmountNano int64  `json:"amount_nano"`
	Payload    string `json:"payload,omitempty"`
	StateInit  string `json:"state_init,omitempty"`
}

// PreparedTransaction is the invoice the signer must reproduce. ValidUntil is nil
// until get*Link is parsed. A zero time.Time is not stored.
type PreparedTransaction struct {
	SenderRaw  string
	ValidUntil *time.Time
	Messages   []OutgoingMessage
}

// SignedMessage is a signed external message. fmt and slog redact the BOC.
type SignedMessage struct {
	MsgHash string // hex hash of the external message cell
	BOC     string // base64
	Seqno   uint32
}

func (m SignedMessage) String() string {
	return redacted + " hash=" + m.MsgHash + " boc_len=" + itoa(len(m.BOC))
}
func (m SignedMessage) GoString() string { return m.String() }
func (m SignedMessage) LogValue() slog.Value {
	return slog.StringValue(m.String())
}

// WalletSigner signs locally. Sign must not dial the network and must not take locks.
// The returned Seqno equals the seqno argument.
type WalletSigner interface {
	Account(ctx context.Context) (Account, error)
	Sign(ctx context.Context, tx PreparedTransaction, seqno uint32) (SignedMessage, error)
}

// LookupStatus is the result of searching for an external message. A network
// failure is LookupUnavailable, not LookupNotFound.
type LookupStatus string

const (
	LookupFound       LookupStatus = "found"
	LookupNotFound    LookupStatus = "not_found"
	LookupUnavailable LookupStatus = "unavailable"
)

// Broadcaster sends an already signed BOC and reads balance and seqno.
// It must not see cookies or the mnemonic. Send is the point of no return:
// if accept and a lost response cannot be told apart, return an error.
type Broadcaster interface {
	Send(ctx context.Context, msg SignedMessage) error
	Lookup(ctx context.Context, msgHash string) (LookupStatus, error)
	BalanceNano(ctx context.Context, rawAddress string) (int64, error)
	Seqno(ctx context.Context, rawAddress string) (uint32, error)
}

// Recipient is a Fragment search result. Token is opaque. It is not a phone number.
type Recipient struct {
	Token    string
	Name     string
	PhotoURL string
	Myself   bool
}

// PreparedPayment is an invoice that has not been signed.
type PreparedPayment struct {
	Key         IdempotencyKey
	State       State
	ReqID       string
	Recipient   Recipient
	Breakdown   AmountBreakdown
	Transaction PreparedTransaction
}

// Payment is the public view of a journal row. It has no BOC.
type Payment struct {
	Key          IdempotencyKey
	State        State
	ReqID        string
	MsgHash      string
	Product      Product
	Username     string
	Quantity     int
	Breakdown    AmountBreakdown
	Confirmed    bool
	ConfirmError string
}

// ResolveOutcome is the only pair of states Resolve may write.
type ResolveOutcome string

const (
	ResolveSettled      ResolveOutcome = "settled"
	ResolveNotBroadcast ResolveOutcome = "not_broadcast"
)
