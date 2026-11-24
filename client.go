package fragment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/andhash/go-fragment/chain"
	"github.com/andhash/go-fragment/internal/protocol"
	"github.com/andhash/go-fragment/internal/transport"
	"github.com/andhash/go-fragment/journal"
	"github.com/andhash/go-fragment/lock"
	"github.com/andhash/go-fragment/wallet"
)

const (
	defaultGasReserve     int64 = 50_000_000
	defaultMaxMessages          = 4
	defaultPrepareTimeout       = 8 * time.Second
	defaultConfirmTimeout       = 60 * time.Second
	prepareRetryAfter           = 30 * time.Second
)

var (
	confirmPollGap = 2 * time.Second
	searchRetryGap = 200 * time.Millisecond
)

// Donation is an explicit extra output. The default is no donation.
// Address is required. BasisPoints and FixedNanoTON cannot both be set.
// BasisPoints is at most 1000. The sweep account is never accepted.
type Donation struct {
	Address      string
	BasisPoints  int
	FixedNanoTON int64
}

// Client buys Stars and gifts Premium from the caller's process.
type Client struct {
	cookies        Cookies
	requireCookies bool
	showSender     bool
	transport      Transport
	signer         WalletSigner
	broadcaster    Broadcaster
	journal        PaymentJournal
	locker         Locker
	logger         *slog.Logger
	metrics        Metrics
	spendLimit     int64
	spendSet       bool
	gasReserve     int64
	treasury       map[string]struct{}
	allowUnpinned  bool
	donation       *Donation
	maxMessages    int
	prepareTimeout time.Duration
	confirmTimeout time.Duration
	clock          func() time.Time
	fixedAccount   *Account
	journalDir     string
	chainEndpoint  string
	chainKey       string
	mnemonic       string
	walletVersion  WalletVersion
	httpDoer       HTTPDoer
}

// Option configures Client.
type Option func(*Client) error

// New builds a client. Omitted WithShowSender leaves ShowSender true.
// An empty cookie set is rejected unless WithTransport is also set.
// An empty HOME without WithJournalDir or WithJournal is an error.
func New(opts ...Option) (*Client, error) {
	c := &Client{
		showSender:     true,
		gasReserve:     defaultGasReserve,
		maxMessages:    defaultMaxMessages,
		prepareTimeout: defaultPrepareTimeout,
		confirmTimeout: defaultConfirmTimeout,
		clock:          time.Now,
		chainEndpoint:  "https://toncenter.com/api/v2",
		walletVersion:  WalletV4R2,
		logger:         slog.New(slog.DiscardHandler),
		metrics:        discardMetrics{},
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	c.logger = slog.New(RedactingHandler{Next: c.logger.Handler()})
	RegisterSecrets(c.cookies.SSID, c.cookies.DT, c.cookies.Token, c.cookies.TONToken, c.mnemonic, c.chainKey)
	if c.transport == nil {
		if !c.cookies.ready() {
			return nil, ErrCookiesRequired
		}
		tr, err := transport.New(transport.Config{
			Cookies: transport.Cookies{SSID: c.cookies.SSID, DT: c.cookies.DT, Token: c.cookies.Token, TONToken: c.cookies.TONToken},
			Doer:    c.httpDoer,
			Now:     c.now,
		})
		if err != nil {
			return nil, err
		}
		c.transport = transportAdapter{c: tr}
		c.requireCookies = true
	}
	if c.signer == nil && c.mnemonic != "" {
		ver := string(c.walletVersion)
		w, err := wallet.New(c.mnemonic, ver)
		if err != nil {
			return nil, err
		}
		c.signer = signerAdapter{w: w}
	}
	if c.journal == nil || c.locker == nil {
		dir := c.journalDir
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil || home == "" {
				return nil, errors.New("HOME is empty and WithJournalDir was not set")
			}
			dir = filepath.Join(home, ".go-fragment")
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return nil, err
		}
		if c.journal == nil {
			f, err := journal.Open(dir)
			if err != nil {
				return nil, err
			}
			c.journal = fileJournal{f: f}
		}
		if c.locker == nil {
			l, err := lock.Open(dir)
			if err != nil {
				return nil, err
			}
			c.locker = l
		}
	}
	if c.broadcaster == nil {
		bc, err := chain.New(c.chainEndpoint, c.chainKey)
		if err != nil {
			return nil, err
		}
		c.broadcaster = chainAdapter{c: bc}
	}
	return c, nil
}

func WithCookies(cookies Cookies) Option {
	return func(c *Client) error {
		c.cookies = cookies
		return nil
	}
}

func WithMnemonic(mnemonic string) Option {
	return func(c *Client) error {
		c.mnemonic = mnemonic
		return nil
	}
}

func WithWallet(version WalletVersion) Option {
	return func(c *Client) error {
		if version != WalletV4R2 && version != WalletV5R1 {
			return errors.New("wallet version must be v4r2 or v5r1")
		}
		c.walletVersion = version
		return nil
	}
}

func WithAccount(a Account) Option {
	return func(c *Client) error {
		cp := a
		c.fixedAccount = &cp
		return nil
	}
}

func WithHTTPDoer(d HTTPDoer) Option {
	return func(c *Client) error {
		c.httpDoer = d
		return nil
	}
}

func WithTransport(t Transport) Option {
	return func(c *Client) error {
		c.transport = t
		return nil
	}
}

func WithSigner(s WalletSigner) Option {
	return func(c *Client) error {
		c.signer = s
		return nil
	}
}

func WithBroadcaster(b Broadcaster) Option {
	return func(c *Client) error {
		c.broadcaster = b
		return nil
	}
}

func WithJournal(j PaymentJournal) Option {
	return func(c *Client) error {
		c.journal = j
		return nil
	}
}

func WithJournalDir(path string) Option {
	return func(c *Client) error {
		if path == "" {
			return errors.New("journal dir is empty")
		}
		c.journalDir = path
		return nil
	}
}

func WithLocker(l Locker) Option {
	return func(c *Client) error {
		c.locker = l
		return nil
	}
}

func WithLogger(l *slog.Logger) Option {
	return func(c *Client) error {
		if l == nil {
			return errors.New("logger is nil")
		}
		c.logger = l
		return nil
	}
}

func WithMetrics(m Metrics) Option {
	return func(c *Client) error {
		if m == nil {
			return errors.New("metrics is nil")
		}
		c.metrics = m
		return nil
	}
}

// WithSpendLimit sets one nanotons ceiling for every Pay. It is not a per-key limit.
func WithSpendLimit(maxNanoTON int64) Option {
	return func(c *Client) error {
		if maxNanoTON <= 0 {
			return errors.New("spend limit must be positive")
		}
		c.spendLimit = maxNanoTON
		c.spendSet = true
		return nil
	}
}

func WithGasReserve(nanoTON int64) Option {
	return func(c *Client) error {
		if nanoTON <= 0 {
			return errors.New("gas reserve must be positive")
		}
		c.gasReserve = nanoTON
		return nil
	}
}

// WithFragmentTreasury pins raw or friendly treasury addresses. Pay checks them. Prepare does not.
func WithFragmentTreasury(rawAddrs ...string) Option {
	return func(c *Client) error {
		if c.treasury == nil {
			c.treasury = map[string]struct{}{}
		}
		for _, raw := range rawAddrs {
			norm, err := NormalizeAddress(raw)
			if err != nil {
				return err
			}
			if err := rejectFragment(norm); err != nil {
				return err
			}
			c.treasury[norm] = struct{}{}
		}
		return nil
	}
}

// WithAllowUnpinnedTreasury skips the treasury pin. It is for a local debug run.
func WithAllowUnpinnedTreasury() Option {
	return func(c *Client) error {
		c.allowUnpinned = true
		return nil
	}
}

// WithDonation adds one explicit output. The default is zero. There is no silent fee.
func WithDonation(d Donation) Option {
	return func(c *Client) error {
		if d.Address == "" {
			return ErrDestinationRejected
		}
		if d.BasisPoints < 0 || d.BasisPoints > 1000 {
			return errors.New("donation basis points must be 0..1000")
		}
		if d.FixedNanoTON < 0 {
			return errors.New("donation amount must be positive")
		}
		if d.BasisPoints > 0 && d.FixedNanoTON > 0 {
			return errors.New("donation basis points and a fixed amount cannot both be set")
		}
		if d.BasisPoints == 0 && d.FixedNanoTON == 0 {
			return errors.New("donation amount is empty")
		}
		raw, err := NormalizeAddress(d.Address)
		if err != nil {
			return err
		}
		if err := rejectDonation(raw); err != nil {
			return err
		}
		d.Address = raw
		c.donation = &d
		return nil
	}
}

func WithMaxFragmentMessages(n int) Option {
	return func(c *Client) error {
		if n < 1 || n > 255 {
			return errors.New("max fragment messages must be 1..255")
		}
		c.maxMessages = n
		return nil
	}
}

func WithPrepareTimeout(d time.Duration) Option {
	return func(c *Client) error {
		if d <= 0 {
			return errors.New("prepare timeout must be positive")
		}
		c.prepareTimeout = d
		return nil
	}
}

func WithConfirmTimeout(d time.Duration) Option {
	return func(c *Client) error {
		if d <= 0 {
			return errors.New("confirm timeout must be positive")
		}
		c.confirmTimeout = d
		return nil
	}
}

func WithChainEndpoint(baseURL string) Option {
	return func(c *Client) error {
		c.chainEndpoint = baseURL
		return nil
	}
}

func WithChainAPIKey(key string) Option {
	return func(c *Client) error {
		c.chainKey = key
		return nil
	}
}

// WithShowSender sets whether Fragment reveals the payer. The default, when this
// option is omitted, is true. The value is part of the idempotency identity.
func WithShowSender(show bool) Option {
	return func(c *Client) error {
		c.showSender = show
		return nil
	}
}

func WithClock(now func() time.Time) Option {
	return func(c *Client) error {
		if now == nil {
			return errors.New("clock is nil")
		}
		c.clock = now
		return nil
	}
}

func (c *Client) now() time.Time {
	if c.clock == nil {
		return time.Now()
	}
	return c.clock()
}

func (c *Client) account(ctx context.Context) (Account, error) {
	if c.signer != nil {
		return c.signer.Account(ctx)
	}
	if c.fixedAccount != nil && c.fixedAccount.Address != "" {
		acc := *c.fixedAccount
		if acc.Chain == "" {
			acc.Chain = "-239"
		}
		raw, err := NormalizeAddress(acc.Address)
		if err != nil {
			return Account{}, err
		}
		acc.Address = raw
		return acc, nil
	}
	return Account{}, ErrAccountRequired
}

func (c *Client) flow(p Product) (protocol.Flow, error) {
	switch p {
	case ProductStars:
		return protocol.Stars, nil
	case ProductPremium:
		return protocol.Premium, nil
	default:
		return protocol.Flow{}, coded("invalid_product", "product must be stars or premium")
	}
}

func keyLockName(key IdempotencyKey) string {
	sum := sha256.Sum256([]byte(key))
	return "id-" + hex.EncodeToString(sum[:])
}

func addrLockName(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (c *Client) lockKey(ctx context.Context, key IdempotencyKey) (func(), error) {
	return c.locker.Acquire(ctx, keyLockName(key))
}

func (c *Client) lockAddr(ctx context.Context, raw string) (func(), error) {
	return c.locker.Acquire(ctx, addrLockName(raw))
}

func (c *Client) save(ctx context.Context, rec Record) (Record, error) {
	rec.Schema = 1
	if rec.ValidUntil != nil && rec.ValidUntil.IsZero() {
		rec.ValidUntil = nil
	}
	if err := c.journal.Store(ctx, rec); err != nil {
		return Record{}, err
	}
	return c.journal.Load(ctx, rec.Key)
}

func (c *Client) budget(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.prepareTimeout)
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) observe(op string, order Order, rec *Record, err error, start time.Time) {
	ev := Event{
		Op:             op,
		Product:        order.Product,
		Result:         CodeOf(err),
		Duration:       c.now().Sub(start),
		IdempotencyKey: string(order.IdempotencyKey),
	}
	attrs := []any{
		"op", op,
		"idempotency_key", string(order.IdempotencyKey),
		"product", string(order.Product),
		"duration_ms", ev.Duration.Milliseconds(),
		"err_code", ev.Result,
	}
	if order.Product == ProductStars {
		attrs = append(attrs, "stars", order.Stars)
	}
	if order.Product == ProductPremium {
		attrs = append(attrs, "months", order.Months)
	}
	if rec != nil {
		ev.ReqID = rec.ReqID
		attrs = append(attrs,
			"req_id", rec.ReqID,
			"state", string(rec.State),
			"payment_nanoton", rec.Breakdown.PaymentNanoTON,
			"required_nanoton", rec.Breakdown.RequiredNanoTON,
		)
		if rec.State == StateBroadcast || rec.State == StateUnknown {
			attrs = append(attrs, "msg_hash", rec.MsgHash)
		}
	}
	c.metrics.Observe(ev)
	c.logger.Info("fragment", attrs...)
}

func toPayment(rec Record) *Payment {
	return &Payment{
		Key:          rec.Key,
		State:        rec.State,
		ReqID:        rec.ReqID,
		MsgHash:      rec.MsgHash,
		Product:      rec.Product,
		Username:     rec.Username,
		Quantity:     rec.Quantity,
		Breakdown:    rec.Breakdown,
		Confirmed:    rec.State == StateConfirmed || rec.State == StateSettled,
		ConfirmError: "",
	}
}

func (c *Client) sameOrder(rec Record, order Order, sender string) bool {
	if rec.Product != order.Product || rec.Username != order.Username || rec.Quantity != order.quantity() || rec.ShowSender != order.ShowSender {
		return false
	}
	if rec.SenderRaw != "" && sender != "" && rec.SenderRaw != sender {
		return false
	}
	addr, basis, fixed := "", 0, int64(0)
	if c.donation != nil {
		addr, basis, fixed = c.donation.Address, c.donation.BasisPoints, c.donation.FixedNanoTON
	}
	return rec.DonationAddress == addr && rec.DonationBasisPoints == basis && rec.DonationFixedNano == fixed
}
