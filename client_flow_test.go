package fragment

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andhash/go-fragment/internal/fixture"
	"github.com/andhash/go-fragment/internal/protocol"
	"github.com/andhash/go-fragment/journal"
	"github.com/andhash/go-fragment/lock"
	"github.com/andhash/go-fragment/wallet"
)

var (
	phraseOnce sync.Once
	phrase     string
	phraseErr  error
)

const (
	treasuryFriendly = "EQAGpMCtSbubdXHzw5wBaAt4Mm9tlvthnHUzqIMcmfdyIQ8s"
	treasuryRaw      = "0:06a4c0ad49bb9b7571f3c39c01680b78326f6d96fb619c7533a8831c99f77221"
	donationFriendly = "EQAUG6HBbY2zeGSVxK1Pr4dTPolVMB7ow4wHpVte5RWjGEOM"
	donationRaw      = "0:141ba1c16d8db3786495c4ad4faf87533e8955301ee8c38c07a55b5ee515a318"
)

func TestMain(m *testing.M) {
	confirmPollGap = time.Millisecond
	searchRetryGap = 0
	os.Exit(m.Run())
}

type lockedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *lockedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *lockedClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

type chainFake struct {
	mu         sync.Mutex
	balance    int64
	seqno      uint32
	seqnoErr   error
	balanceErr error
	sendErr    error
	lookup     LookupStatus
	lookupErr  error
	sends      int
	calls      int
}

func (f *chainFake) Send(context.Context, SignedMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.sends++
	return f.sendErr
}

func (f *chainFake) Lookup(context.Context, string) (LookupStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.lookup == "" {
		return LookupNotFound, f.lookupErr
	}
	return f.lookup, f.lookupErr
}

func (f *chainFake) BalanceNano(context.Context, string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.balanceErr != nil {
		return 0, f.balanceErr
	}
	return f.balance, nil
}

func (f *chainFake) Seqno(context.Context, string) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.seqnoErr != nil {
		return 0, f.seqnoErr
	}
	return f.seqno, nil
}

func (f *chainFake) Sends() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sends
}

type countingSigner struct {
	inner WalletSigner
	mu    sync.Mutex
	signs int
	extra bool
}

func (s *countingSigner) Account(ctx context.Context) (Account, error) {
	return s.inner.Account(ctx)
}

func (s *countingSigner) Sign(ctx context.Context, tx PreparedTransaction, seqno uint32) (SignedMessage, error) {
	s.mu.Lock()
	s.signs++
	extra := s.extra
	s.mu.Unlock()
	if extra {
		tx.Messages = append(append([]OutgoingMessage{}, tx.Messages...), OutgoingMessage{Address: donationRaw, AmountNano: 1})
	}
	return s.inner.Sign(ctx, tx, seqno)
}

func (s *countingSigner) Signs() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signs
}

type orderLock struct {
	inner  Locker
	mu     sync.Mutex
	log    []string
	onAddr func()
}

func (l *orderLock) Acquire(ctx context.Context, name string) (func(), error) {
	if l.onAddr != nil && !strings.HasPrefix(name, "id-") {
		l.onAddr()
	}
	rel, err := l.inner.Acquire(ctx, name)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.log = append(l.log, "acquire "+name)
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		l.log = append(l.log, "release "+name)
		l.mu.Unlock()
		rel()
	}, nil
}

func (l *orderLock) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.log))
	copy(out, l.log)
	return out
}

type failStore struct {
	PaymentJournal
	mu   sync.Mutex
	fail func(Record) bool
}

func (f *failStore) Store(ctx context.Context, rec Record) error {
	f.mu.Lock()
	fn := f.fail
	f.mu.Unlock()
	if fn != nil && fn(rec) {
		return withCause(ErrJournal, errors.New("disk"))
	}
	return f.PaymentJournal.Store(ctx, rec)
}

type blankJournal struct {
	PaymentJournal
	mu    sync.Mutex
	armed bool
	loads int
}

func (b *blankJournal) Load(ctx context.Context, key IdempotencyKey) (Record, error) {
	rec, err := b.PaymentJournal.Load(ctx, key)
	if err != nil {
		return rec, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.armed {
		return rec, nil
	}
	b.loads++
	if b.loads > 1 {
		rec.ValidUntil = nil
		rec.Messages = nil
	}
	return rec, nil
}

type env struct {
	t        *testing.T
	c        *Client
	player   *fixture.Player
	chain    *chainFake
	signer   *countingSigner
	locks    *orderLock
	dir      string
	clock    *lockedClock
	mnemonic string
}

func newEnv(t *testing.T, script string, opts ...Option) *env {
	t.Helper()
	p, err := fixture.Load("testdata/fixtures/stars_buy.html", script)
	if err != nil {
		t.Fatal(err)
	}
	return buildEnv(t, p, opts...)
}

func buildEnv(t *testing.T, p *fixture.Player, opts ...Option) *env {
	t.Helper()
	dir := t.TempDir()
	clk := &lockedClock{t: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
	phraseOnce.Do(func() {
		phrase, phraseErr = wallet.GenerateMnemonic()
	})
	if phraseErr != nil {
		t.Fatal(phraseErr)
	}
	ch := &chainFake{balance: 10_000_000_000, seqno: 1, lookup: LookupNotFound}
	jf, err := journal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	lk, err := lock.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ol := &orderLock{inner: lk}
	base := []Option{
		WithCookies(Cookies{SSID: "COOKIEMARKER", DT: "DTMARKER", Token: "tok-token", TONToken: "ton-token"}),
		WithHTTPDoer(p),
		WithMnemonic(phrase),
		WithJournal(fileJournal{f: jf}),
		WithLocker(ol),
		WithBroadcaster(ch),
		WithClock(clk.Now),
		WithSpendLimit(2_000_000_000),
		WithFragmentTreasury(treasuryFriendly),
	}
	c, err := New(append(base, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	cs := &countingSigner{inner: c.signer}
	c.signer = cs
	return &env{t: t, c: c, player: p, chain: ch, signer: cs, locks: ol, dir: dir, clock: clk, mnemonic: phrase}
}

func (e *env) row(key IdempotencyKey) journal.Row {
	e.t.Helper()
	jf, err := journal.Open(e.dir)
	if err != nil {
		e.t.Fatal(err)
	}
	row, err := jf.Load(context.Background(), string(key))
	if err != nil {
		e.t.Fatal(err)
	}
	return row
}

func (e *env) secretLeak() {
	e.t.Helper()
	for _, call := range e.player.Calls() {
		if strings.Contains(call.Body, e.mnemonic) || strings.Contains(call.URL, e.mnemonic) {
			e.t.Fatal("mnemonic left the process")
		}
		if call.Header.Get("Cookie") != "" {
			e.t.Fatal("sanitized call kept a cookie")
		}
	}
	if !e.player.SawCookie() {
		e.t.Fatal("fragment.com did not receive the session cookie")
	}
}

func countMethod(p *fixture.Player, name string) int {
	n := 0
	for _, m := range p.Methods() {
		if m == name {
			n++
		}
	}
	return n
}

func formOf(p *fixture.Player, method string) map[string][]string {
	var last map[string][]string
	for _, c := range p.Calls() {
		if c.FormMethod == method {
			last = c.Form
		}
	}
	return last
}

func TestStarsPrepareFixture(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	pp, err := e.c.PrepareStars(context.Background(), "https://t.me/alice", 50, "order-1001")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{protocol.Stars.StateMethod, protocol.Stars.SearchMethod, protocol.Stars.InitMethod, protocol.Stars.LinkMethod}
	if strings.Join(e.player.Methods(), ",") != strings.Join(want, ",") {
		t.Fatalf("methods %v", e.player.Methods())
	}
	if pp.State != StatePrepared || pp.ReqID != "req-stars-1" {
		t.Fatalf("prepared %+v", pp)
	}
	if pp.Breakdown.PaymentNanoTON != 500_000_000 || pp.Breakdown.RequiredNanoTON != 550_000_000 || pp.Breakdown.DonationNanoTON != 0 {
		t.Fatalf("quote %+v", pp.Breakdown)
	}
	link := formOf(e.player, protocol.Stars.LinkMethod)
	if link["show_sender"][0] != "1" || link["device"][0] != protocol.DeviceFingerprint || link["lv"] != nil {
		t.Fatalf("link form %#v", link)
	}
	state := formOf(e.player, protocol.Stars.StateMethod)
	if state["mode"][0] != "new" || state["lv"][0] != "false" {
		t.Fatalf("state form %#v", state)
	}
	if formOf(e.player, protocol.Stars.InitMethod)["quantity"][0] != "50" {
		t.Fatal("quantity")
	}
	row := e.row("order-1001")
	if row.InitAttempts != 1 || row.ValidUntil == nil || row.ValidUntil.IsZero() {
		t.Fatalf("row attempts %d until %v", row.InitAttempts, row.ValidUntil)
	}
	if _, err := e.c.PrepareStars(context.Background(), "@alice", 50, "order-1001"); err != nil {
		t.Fatal(err)
	}
	if countMethod(e.player, protocol.Stars.InitMethod) != 1 || countMethod(e.player, protocol.Stars.LinkMethod) != 1 || countMethod(e.player, protocol.Stars.StateMethod) != 1 {
		t.Fatalf("second prepare called the network: %v", e.player.Methods())
	}
	e.secretLeak()
	for _, call := range e.player.Calls() {
		if !strings.Contains(call.URL, "hash=0123456789abcdef") && call.Method == "POST" {
			t.Fatalf("post without fixture hash: %s", call.URL)
		}
	}
}

func TestDonationQuote(t *testing.T) {
	n, err := PercentNano(500_000_000, 150)
	if err != nil || n != 7_500_000 {
		t.Fatalf("percent %d %v", n, err)
	}
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json", WithDonation(Donation{Address: donationFriendly, BasisPoints: 150}))
	pp, err := e.c.PrepareStars(context.Background(), "alice", 50, "don-1")
	if err != nil {
		t.Fatal(err)
	}
	if pp.Breakdown.DonationNanoTON != 7_500_000 || pp.Breakdown.RequiredNanoTON != 557_500_000 {
		t.Fatalf("quote %+v", pp.Breakdown)
	}
	if pp.Breakdown.RequiredNanoTON == 565_000_000 {
		t.Fatal("gas was counted twice")
	}
	pay, err := e.c.PayStars(context.Background(), "alice", 50, "don-1")
	if err != nil || pay.State != StateBroadcast {
		t.Fatalf("pay %+v %v", pay, err)
	}
	if e.chain.Sends() != 1 || countMethod(e.player, "confirmReq") != 0 {
		t.Fatalf("sends %d methods %v", e.chain.Sends(), e.player.Methods())
	}
}

func TestShowSenderIsIdentity(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json", WithShowSender(false))
	if _, err := e.c.PrepareStars(context.Background(), "alice", 50, "show-1"); err != nil {
		t.Fatal(err)
	}
	if formOf(e.player, protocol.Stars.LinkMethod)["show_sender"][0] != "0" {
		t.Fatal("show_sender")
	}
	_, err := e.c.Prepare(context.Background(), Order{Product: ProductStars, Username: "alice", Stars: 50, ShowSender: true, IdempotencyKey: "show-1"})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict %v", err)
	}
}

func TestOpaqueKeyAcceptsAnyPrefix(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	if _, err := e.c.PrepareStars(context.Background(), "alice", 50, "ggsel:1"); err != nil {
		t.Fatal(err)
	}
}

func TestPremiumMonthsRejectedBeforeNetwork(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/premium_prepare_ok.json")
	if _, err := e.c.PreparePremium(context.Background(), "alice", 1, "m-1"); !errors.Is(err, ErrInvalidMonths) {
		t.Fatalf("%v", err)
	}
	if len(e.player.Calls()) != 0 {
		t.Fatal("network was called")
	}
}

func TestPremiumPrepareAndAlreadySubscribed(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/premium_prepare_ok.json")
	pp, err := e.c.PreparePremium(context.Background(), "alice", 3, "prem-1")
	if err != nil {
		t.Fatal(err)
	}
	if pp.Breakdown.Quantity != 3 {
		t.Fatalf("quantity %d", pp.Breakdown.Quantity)
	}
	want := []string{protocol.Premium.StateMethod, protocol.Premium.SearchMethod, protocol.Premium.InitMethod, protocol.Premium.LinkMethod}
	if strings.Join(e.player.Methods(), ",") != strings.Join(want, ",") {
		t.Fatalf("methods %v", e.player.Methods())
	}
	if formOf(e.player, protocol.Premium.InitMethod)["months"][0] != "3" {
		t.Fatal("months")
	}

	p, err := fixture.Load("testdata/fixtures/stars_buy.html", "testdata/fixtures/premium_prepare_ok.json")
	if err != nil {
		t.Fatal(err)
	}
	p.Insert(fixture.Step{Method: protocol.Premium.SearchMethod, Status: 200, Body: json.RawMessage(`{"error":"This account is already subscribed"}`)})
	e2 := buildEnv(t, p)
	_, err = e2.c.PreparePremium(context.Background(), "alice", 12, "prem-sub")
	if !errors.Is(err, ErrAlreadySubscribed) {
		t.Fatalf("%v", err)
	}
}

func TestPhoneRejectedBeforeNetwork(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	if _, err := e.c.PrepareStars(context.Background(), "+79991234567", 50, "phone"); !errors.Is(err, ErrPhoneNotAccepted) {
		t.Fatalf("%v", err)
	}
	if len(e.player.Calls()) != 0 {
		t.Fatal("network was called")
	}
}

func TestBadRequestRefreshDoesNotBumpInitAttempts(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	e.player.Insert(fixture.Step{Method: protocol.Stars.InitMethod, Status: 200, Body: json.RawMessage(`{"error":"bad request"}`)})
	if _, err := e.c.PrepareStars(context.Background(), "alice", 50, "hash-1"); err != nil {
		t.Fatal(err)
	}
	if countMethod(e.player, protocol.Stars.InitMethod) != 2 {
		t.Fatalf("init posts %d", countMethod(e.player, protocol.Stars.InitMethod))
	}
	if e.row("hash-1").InitAttempts != 1 {
		t.Fatalf("attempts %d", e.row("hash-1").InitAttempts)
	}
}

func TestPrepareStuckAfterOneRetry(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	e.player.Insert(fixture.Step{Method: protocol.Stars.InitMethod, Drop: true})
	e.player.Insert(fixture.Step{Method: protocol.Stars.InitMethod, Drop: true})
	e.player.Fallback(protocol.Stars.StateMethod, 200, `{"ok":true}`)
	e.player.Fallback(protocol.Stars.SearchMethod, 200, `{"found":{"recipient":"recipient-token","name":"Ada"}}`)
	if _, err := e.c.PrepareStars(context.Background(), "alice", 50, "stuck-1"); err == nil {
		t.Fatal("init drop succeeded")
	}
	_, err := e.c.PrepareStars(context.Background(), "alice", 50, "stuck-1")
	if !errors.Is(err, ErrPrepareInProgress) {
		t.Fatalf("in progress %v", err)
	}
	e.clock.Set(e.clock.Now().Add(31 * time.Second))
	if _, err := e.c.PrepareStars(context.Background(), "alice", 50, "stuck-1"); err == nil {
		t.Fatal("second init drop succeeded")
	}
	if countMethod(e.player, protocol.Stars.InitMethod) != 2 {
		t.Fatalf("init calls %d", countMethod(e.player, protocol.Stars.InitMethod))
	}
	_, err = e.c.PrepareStars(context.Background(), "alice", 50, "stuck-1")
	if !errors.Is(err, ErrPrepareStuck) {
		t.Fatalf("stuck %v", err)
	}
	if countMethod(e.player, protocol.Stars.InitMethod) != 2 {
		t.Fatal("stuck path called init")
	}
}

func TestNilValidUntilRefetchesLinkOnly(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	if _, err := e.c.PrepareStars(context.Background(), "alice", 50, "nil-until"); err != nil {
		t.Fatal(err)
	}
	jf, err := journal.Open(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	row, err := jf.Load(context.Background(), "nil-until")
	if err != nil {
		t.Fatal(err)
	}
	row.ValidUntil = nil
	row.Messages = []byte("[]")
	if err := jf.Store(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	e.player.Insert(fixture.Step{Method: protocol.Stars.LinkMethod, Status: 200, Body: json.RawMessage(linkBody())})
	pp, err := e.c.PrepareStars(context.Background(), "alice", 50, "nil-until")
	if err != nil {
		t.Fatal(err)
	}
	if errors.Is(err, ErrInvoiceExpired) || pp.Transaction.ValidUntil == nil {
		t.Fatalf("expired or still nil: %+v %v", pp, err)
	}
	if countMethod(e.player, protocol.Stars.InitMethod) != 1 || countMethod(e.player, protocol.Stars.StateMethod) != 1 || countMethod(e.player, protocol.Stars.LinkMethod) != 2 {
		t.Fatalf("methods %v", e.player.Methods())
	}
}

func TestExpiredInvoiceNeedsANewKey(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	if _, err := e.c.PrepareStars(context.Background(), "alice", 50, "exp-1"); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(time.Unix(1893456000, 0).Add(time.Second))
	_, err := e.c.PayStars(context.Background(), "alice", 50, "exp-1")
	if !errors.Is(err, ErrInvoiceExpired) {
		t.Fatalf("%v", err)
	}
	if countMethod(e.player, protocol.Stars.InitMethod) != 1 || e.signer.Signs() != 0 || e.chain.Sends() != 0 {
		t.Fatalf("init %d signs %d sends %d", countMethod(e.player, protocol.Stars.InitMethod), e.signer.Signs(), e.chain.Sends())
	}
}

func TestRereadNilValidUntilDoesNotSign(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	blank := &blankJournal{PaymentJournal: e.c.journal}
	e.c.journal = blank
	if _, err := e.c.PrepareStars(context.Background(), "alice", 50, "step16"); err != nil {
		t.Fatal(err)
	}
	blank.armed = true
	_, err := e.c.PayStars(context.Background(), "alice", 50, "step16")
	if err == nil || e.signer.Signs() != 0 || e.chain.Sends() != 0 {
		t.Fatalf("err %v signs %d sends %d", err, e.signer.Signs(), e.chain.Sends())
	}
	if countMethod(e.player, protocol.Stars.InitMethod) != 1 || countMethod(e.player, protocol.Stars.LinkMethod) != 2 {
		t.Fatalf("methods %v", e.player.Methods())
	}
	assertAddrReleasedFirst(t, e.locks.snapshot())
}

func TestPayBroadcastDoesNotConfirm(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json", WithWallet(WalletV5R1))
	pay, err := e.c.PayStars(context.Background(), "alice", 50, "pay-v5")
	if err != nil || pay.State != StateBroadcast {
		t.Fatalf("%+v %v", pay, err)
	}
	if countMethod(e.player, "confirmReq") != 0 || e.chain.Sends() != 1 || e.signer.Signs() != 1 {
		t.Fatalf("sends %d signs %d methods %v", e.chain.Sends(), e.signer.Signs(), e.player.Methods())
	}
	again, err := e.c.PayStars(context.Background(), "alice", 50, "pay-v5")
	if err != nil || again.State != StateBroadcast || e.signer.Signs() != 1 {
		t.Fatalf("second %+v %v signs %d", again, err, e.signer.Signs())
	}
	calls := e.chain.calls
	if _, err := e.c.Get(context.Background(), "pay-v5"); err != nil {
		t.Fatal(err)
	}
	if e.chain.calls != calls {
		t.Fatal("Get called the chain")
	}
	e.secretLeak()
}

func TestRetryAfterAmbiguousSendDoesNotInit(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	e.chain.sendErr = errors.New("connection reset")
	_, err := e.c.PayStars(context.Background(), "alice", 50, "amb-1")
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("%v", err)
	}
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) || unknown.MsgHash == "" {
		t.Fatalf("as %#v", err)
	}
	if e.row("amb-1").State != string(StateUnknown) || e.row("amb-1").SendAttempted != true {
		t.Fatalf("row %+v", e.row("amb-1"))
	}
	_, err = e.c.PayStars(context.Background(), "alice", 50, "amb-1")
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("second %v", err)
	}
	if countMethod(e.player, protocol.Stars.InitMethod) != 1 || e.chain.Sends() != 1 || e.signer.Signs() != 1 {
		t.Fatalf("init %d sends %d signs %d", countMethod(e.player, protocol.Stars.InitMethod), e.chain.Sends(), e.signer.Signs())
	}
}

func TestSeqnoUnavailableStaysPrepared(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	e.chain.seqnoErr = errors.New("seqno down")
	_, err := e.c.PayStars(context.Background(), "alice", 50, "seq-1")
	if !errors.Is(err, ErrSeqnoUnavailable) || !RetrySameKey(err) {
		t.Fatalf("%v retry %v", err, RetrySameKey(err))
	}
	if e.signer.Signs() != 0 || e.chain.Sends() != 0 || e.row("seq-1").State != string(StatePrepared) {
		t.Fatalf("signs %d state %s", e.signer.Signs(), e.row("seq-1").State)
	}
	if errors.Is(err, ErrChainUnavailable) || errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal("seqno failure was classified as chain or unknown")
	}
}

func TestSpendLimitBalanceAndPin(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	e.c.spendSet = false
	_, err := e.c.PayStars(context.Background(), "alice", 50, "lim-1")
	if !errors.Is(err, ErrSpendLimit) || e.signer.Signs() != 0 {
		t.Fatalf("unset %v signs %d", err, e.signer.Signs())
	}
	e.c.spendSet = true
	e.c.spendLimit = 1
	_, err = e.c.PayStars(context.Background(), "alice", 50, "lim-1")
	if !errors.Is(err, ErrSpendLimit) {
		t.Fatalf("low %v", err)
	}
	e.c.spendLimit = 2_000_000_000
	e.c.treasury = map[string]struct{}{}
	_, err = e.c.PayStars(context.Background(), "alice", 50, "lim-1")
	if !errors.Is(err, ErrTreasuryUnpinned) {
		t.Fatalf("pin %v", err)
	}
	e.c.treasury = map[string]struct{}{donationRaw: {}}
	_, err = e.c.PayStars(context.Background(), "alice", 50, "lim-1")
	if !errors.Is(err, ErrDestinationRejected) || e.signer.Signs() != 0 {
		t.Fatalf("dest %v signs %d", err, e.signer.Signs())
	}
	e.c.treasury = map[string]struct{}{treasuryRaw: {}}
	e.chain.balance = 1
	_, err = e.c.PayStars(context.Background(), "alice", 50, "lim-1")
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("balance %v", err)
	}
	e.chain.balance = 10_000_000_000
	e.chain.balanceErr = errors.New("balance down")
	_, err = e.c.PayStars(context.Background(), "alice", 50, "lim-1")
	if !errors.Is(err, ErrBalanceUnavailable) || !RetrySameKey(err) {
		t.Fatalf("unavailable %v", err)
	}
}

func TestJournalFailureSkipsSend(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	fs := &failStore{PaymentJournal: e.c.journal, fail: func(r Record) bool { return r.State == StateSigned }}
	e.c.journal = fs
	_, err := e.c.PayStars(context.Background(), "alice", 50, "disk-1")
	if !errors.Is(err, ErrJournal) || e.chain.Sends() != 0 {
		t.Fatalf("%v sends %d", err, e.chain.Sends())
	}
	if e.row("disk-1").State != string(StatePrepared) || e.row("disk-1").BOC != "" {
		t.Fatal("boc stored despite a failed save")
	}
}

func TestNotSentThenResume(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	fs := &failStore{PaymentJournal: e.c.journal, fail: func(r Record) bool { return r.SendAttempted }}
	e.c.journal = fs
	_, err := e.c.PayStars(context.Background(), "alice", 50, "resume-1")
	if !errors.Is(err, ErrJournal) || e.chain.Sends() != 0 {
		t.Fatalf("pay %v sends %d", err, e.chain.Sends())
	}
	_, err = e.c.PayStars(context.Background(), "alice", 50, "resume-1")
	if !errors.Is(err, ErrNotSent) {
		t.Fatalf("not sent %v", err)
	}
	fs.fail = func(Record) bool { return false }
	e.chain.lookup = LookupUnavailable
	_, err = e.c.Resume(context.Background(), "resume-1")
	if !errors.Is(err, ErrChainUnavailable) || e.chain.Sends() != 0 || e.row("resume-1").SendAttempted {
		t.Fatalf("unavailable %v flag %v", err, e.row("resume-1").SendAttempted)
	}
	e.chain.lookup = LookupFound
	_, err = e.c.Resume(context.Background(), "resume-1")
	if !errors.Is(err, ErrResumeRefused) || e.row("resume-1").SendAttempted {
		t.Fatalf("found %v", err)
	}
	e.chain.lookup = LookupNotFound
	pay, err := e.c.Resume(context.Background(), "resume-1")
	if err != nil || pay.State != StateBroadcast || e.chain.Sends() != 1 {
		t.Fatalf("resume %+v %v sends %d", pay, err, e.chain.Sends())
	}
	_, err = e.c.Resume(context.Background(), "resume-1")
	if !errors.Is(err, ErrOutcomeUnknown) || e.chain.Sends() != 1 {
		t.Fatalf("second resume %v sends %d", err, e.chain.Sends())
	}
}

func TestResumeExpiredClosesWithResolve(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	fs := &failStore{PaymentJournal: e.c.journal, fail: func(r Record) bool { return r.SendAttempted }}
	e.c.journal = fs
	if _, err := e.c.PayStars(context.Background(), "alice", 50, "old-1"); !errors.Is(err, ErrJournal) {
		t.Fatal(err)
	}
	fs.fail = func(Record) bool { return false }
	e.clock.Set(time.Unix(1893456000, 0).Add(time.Hour))
	_, err := e.c.Resume(context.Background(), "old-1")
	if !errors.Is(err, ErrResumeRefused) || e.chain.Sends() != 0 {
		t.Fatalf("resume %v sends %d", err, e.chain.Sends())
	}
	_, err = e.c.Resolve(context.Background(), "old-1", ResolveNotBroadcast, "")
	if !errors.Is(err, ErrResolveRefused) {
		t.Fatalf("empty evidence %v", err)
	}
	e.chain.lookup = LookupFound
	_, err = e.c.Resolve(context.Background(), "old-1", ResolveNotBroadcast, "scanner")
	if !errors.Is(err, ErrResolveRefused) || e.row("old-1").State == string(StateNotBroadcast) {
		t.Fatalf("found changed state %s %v", e.row("old-1").State, err)
	}
	e.chain.lookup = LookupNotFound
	pay, err := e.c.Resolve(context.Background(), "old-1", ResolveNotBroadcast, "scanner")
	if err != nil || pay.State != StateNotBroadcast {
		t.Fatalf("resolve %+v %v", pay, err)
	}
}

func TestWalletBusyAndBreakdownMismatch(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	if _, err := e.c.PayStars(context.Background(), "alice", 50, "busy-1"); err != nil {
		t.Fatal(err)
	}
	e.player.Fallback(protocol.Stars.StateMethod, 200, `{"ok":true}`)
	e.player.Fallback(protocol.Stars.SearchMethod, 200, `{"found":{"recipient":"recipient-token","name":"Ada"}}`)
	e.player.Fallback(protocol.Stars.InitMethod, 200, `{"req_id":"req-stars-2","amount":"0.5"}`)
	e.player.Fallback(protocol.Stars.LinkMethod, 200, linkBody())
	signs := e.signer.Signs()
	_, err := e.c.PayStars(context.Background(), "alice", 50, "busy-2")
	if !errors.Is(err, ErrWalletBusy) || e.signer.Signs() != signs {
		t.Fatalf("busy %v signs %d want %d", err, e.signer.Signs(), signs)
	}
	assertNoKeyUnderAddress(t, e.locks.snapshot())

	e2 := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	e2.signer.extra = true
	_, err = e2.c.PayStars(context.Background(), "alice", 50, "extra-1")
	if !errors.Is(err, ErrBreakdownMismatch) || e2.chain.Sends() != 0 {
		t.Fatalf("mismatch %v sends %d", err, e2.chain.Sends())
	}
	assertAddrReleasedFirst(t, e2.locks.snapshot())
	if e2.row("extra-1").BOC != "" {
		t.Fatal("mismatch stored a boc")
	}
}

func TestRereadPastValidUntilReleasesAddressFirst(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	if _, err := e.c.PrepareStars(context.Background(), "alice", 50, "past-lock"); err != nil {
		t.Fatal(err)
	}
	e.locks.onAddr = func() {
		e.clock.Set(time.Unix(1893456000, 0).Add(time.Second))
	}
	_, err := e.c.PayStars(context.Background(), "alice", 50, "past-lock")
	if !errors.Is(err, ErrInvoiceExpired) || e.signer.Signs() != 0 || e.chain.Sends() != 0 {
		t.Fatalf("%v signs %d", err, e.signer.Signs())
	}
	assertAddrReleasedFirst(t, e.locks.snapshot())
}

func TestReconcileConfirmAndTimeout(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	if _, err := e.c.PayStars(context.Background(), "alice", 50, "conf-1"); err != nil {
		t.Fatal(err)
	}
	e.player.Insert(fixture.Step{Method: "confirmReq", Status: 500, Body: json.RawMessage(`{"error":"down"}`)})
	_, err := e.c.Reconcile(context.Background(), "conf-1")
	if !errors.Is(err, ErrConfirmUnavailable) || errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("confirm %v", err)
	}
	if e.row("conf-1").State != string(StateBroadcast) || countMethod(e.player, protocol.Stars.InitMethod) != 1 {
		t.Fatalf("state %s", e.row("conf-1").State)
	}
	e.player.Insert(fixture.Step{Method: "confirmReq", Status: 200, Body: json.RawMessage(`{"ok":true}`)})
	e.player.Insert(fixture.Step{Method: protocol.Stars.StateMethod, Status: 200, Body: json.RawMessage(`{"mode":"done","need_update":false,"req_id":"req-stars-1"}`)})
	pay, err := e.c.Reconcile(context.Background(), "conf-1")
	if err != nil || pay.State != StateConfirmed {
		t.Fatalf("done %+v %v", pay, err)
	}
	modes := []string{}
	for _, c := range e.player.Calls() {
		if c.FormMethod == protocol.Stars.StateMethod {
			modes = append(modes, c.Form.Get("mode"))
		}
	}
	if modes[0] != "new" {
		t.Fatalf("first poll mode %v", modes)
	}
	if countMethod(e.player, "confirmReq") != 2 || e.chain.Sends() != 1 {
		t.Fatalf("confirm posts %d sends %d", countMethod(e.player, "confirmReq"), e.chain.Sends())
	}

	e2 := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	e2.c.clock = time.Now
	e2.c.confirmTimeout = 200 * time.Millisecond
	if _, err := e2.c.PayStars(context.Background(), "alice", 50, "conf-time"); err != nil {
		t.Fatal(err)
	}
	e2.player.Fallback("confirmReq", 200, `{"ok":true}`)
	e2.player.Fallback(protocol.Stars.StateMethod, 200, `{"mode":"processing","need_update":true,"req_id":"req-stars-1"}`)
	_, err = e2.c.Reconcile(context.Background(), "conf-time")
	if !errors.Is(err, ErrConfirmationTimeout) || e2.row("conf-time").State != string(StateBroadcast) {
		t.Fatalf("timeout %v state %s", err, e2.row("conf-time").State)
	}
}

func TestNoteSeqnoAndResolveSettled(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	if _, err := e.c.PayStars(context.Background(), "alice", 50, "seqnote"); err != nil {
		t.Fatal(err)
	}
	e.chain.lookup = LookupNotFound
	e.chain.seqno = 5
	e.player.Insert(fixture.Step{Method: "confirmReq", Status: 500, Body: json.RawMessage(`{"error":"down"}`)})
	if _, err := e.c.Reconcile(context.Background(), "seqnote"); !errors.Is(err, ErrConfirmUnavailable) {
		t.Fatal(err)
	}
	if e.row("seqnote").SeqnoAfter == nil || *e.row("seqnote").SeqnoAfter != 5 {
		t.Fatalf("seqno_after %v", e.row("seqnote").SeqnoAfter)
	}

	e2 := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	e2.chain.sendErr = errors.New("reset")
	if _, err := e2.c.PayStars(context.Background(), "alice", 50, "settle-1"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	e2.chain.seqnoErr = errors.New("seqno down")
	_, recErr := e2.c.Reconcile(context.Background(), "settle-1")
	if !errors.Is(recErr, ErrOutcomeUnknown) || errors.Is(recErr, ErrSeqnoUnavailable) {
		t.Fatalf("reconcile %v", recErr)
	}
	if e2.row("settle-1").SeqnoAfter != nil {
		t.Fatal("seqno error was stored")
	}
	_, err := e2.c.Resolve(context.Background(), "settle-1", ResolveSettled, "note")
	if !errors.Is(err, ErrResolveRefused) || e2.row("settle-1").State != string(StateUnknown) {
		t.Fatalf("refused %v state %s", err, e2.row("settle-1").State)
	}
	e2.chain.lookup = LookupFound
	pay, err := e2.c.Resolve(context.Background(), "settle-1", ResolveSettled, "tx in block")
	if err != nil || pay.State != StateSettled {
		t.Fatalf("settled %+v %v", pay, err)
	}
}

func TestSweepAndFeeDenylist(t *testing.T) {
	if _, err := New(WithCookies(testCookies()), WithTransport(stubTransport{}), WithAccount(Account{Address: treasuryRaw}), WithJournalDir(t.TempDir()), WithDonation(Donation{Address: sweepFriendly, BasisPoints: 1})); err == nil {
		t.Fatal("sweep donation accepted")
	}
	p := fixture.New([]byte(`<html>/api?hash=0123456789abcdef</html>`), []fixture.Step{
		{Method: protocol.Stars.StateMethod, Body: json.RawMessage(`{"ok":true}`)},
		{Method: protocol.Stars.SearchMethod, Body: json.RawMessage(`{"found":{"recipient":"recipient-token","name":"Ada"}}`)},
		{Method: protocol.Stars.InitMethod, Body: json.RawMessage(`{"req_id":"req-fee","amount":"0.5"}`)},
		{Method: protocol.Stars.LinkMethod, Body: json.RawMessage(`{"transaction":{"validUntil":1893456000,"from":"@account","messages":[{"address":"` + feeFriendly + `","amount":"500000000"}]}}`)},
	})
	e := buildEnv(t, p)
	if _, err := e.c.PrepareStars(context.Background(), "alice", 50, "fee-msg"); !errors.Is(err, ErrDestinationRejected) {
		t.Fatalf("fee as fragment %v", err)
	}
	ok, err := New(WithCookies(testCookies()), WithTransport(stubTransport{}), WithAccount(Account{Address: treasuryRaw}), WithJournalDir(t.TempDir()), WithDonation(Donation{Address: feeFriendly, FixedNanoTON: 1}))
	if err != nil || ok == nil {
		t.Fatal(err)
	}
}

func TestBalanceWrapper(t *testing.T) {
	e := newEnv(t, "testdata/fixtures/stars_prepare_ok.json")
	n, err := e.c.Balance(context.Background())
	if err != nil || n != 10_000_000_000 {
		t.Fatalf("%d %v", n, err)
	}
	e.chain.balanceErr = errors.New("down")
	if _, err := e.c.Balance(context.Background()); !errors.Is(err, ErrBalanceUnavailable) {
		t.Fatalf("%v", err)
	}
}

func TestTypedErrorsMatchSentinels(t *testing.T) {
	cases := []error{
		&InvoiceExpiredError{Key: "k"},
		&OutcomeUnknownError{Key: "k", MsgHash: "ab"},
		&NotSentError{Key: "k", MsgHash: "ab"},
		&ConfirmationTimeoutError{Payment: &Payment{Key: "k"}},
		&NotBroadcastError{Payment: &Payment{Key: "k"}},
	}
	sentinels := []error{ErrInvoiceExpired, ErrOutcomeUnknown, ErrNotSent, ErrConfirmationTimeout, ErrNotBroadcast}
	for i, err := range cases {
		if !errors.Is(err, sentinels[i]) {
			t.Fatalf("%v is not %v", err, sentinels[i])
		}
	}
}

type stubTransport struct{}

func (stubTransport) Call(context.Context, Call) (Reply, error) {
	return Reply{HTTPStatus: 200, Body: json.RawMessage(`{"ok":true}`)}, nil
}

func testCookies() Cookies {
	return Cookies{SSID: "ssid-value", DT: "dt-value", Token: "token-value", TONToken: "ton-value"}
}

func linkBody() string {
	return `{"transaction":{"validUntil":1893456000,"from":"@account","messages":[{"address":"` + treasuryFriendly + `","amount":"500000000"}]}}`
}

func assertAddrReleasedFirst(t *testing.T, log []string) {
	t.Helper()
	for i, line := range log {
		if strings.HasPrefix(line, "acquire ") && !strings.HasPrefix(line, "acquire id-") {
			if i+1 >= len(log) || !strings.HasPrefix(log[i+1], "release ") || strings.HasPrefix(log[i+1], "release id-") {
				t.Fatalf("address lock not released first: %v", log)
			}
			if i+2 >= len(log) || !strings.HasPrefix(log[i+2], "release id-") {
				t.Fatalf("key lock not released after the address: %v", log)
			}
			return
		}
	}
	t.Fatalf("address lock was not taken: %v", log)
}

func assertNoKeyUnderAddress(t *testing.T, log []string) {
	t.Helper()
	held := 0
	for _, line := range log {
		switch {
		case strings.HasPrefix(line, "acquire ") && !strings.HasPrefix(line, "acquire id-"):
			held++
		case strings.HasPrefix(line, "release ") && !strings.HasPrefix(line, "release id-"):
			held--
		case held > 0 && strings.HasPrefix(line, "acquire id-"):
			t.Fatalf("key acquired under the address lock: %v", log)
		}
	}
}
