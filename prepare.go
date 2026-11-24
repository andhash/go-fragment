package fragment

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/andhash/go-fragment/internal/protocol"
)

type nextStep int

const (
	stepSign nextStep = iota
	stepInProgress
	stepStuck
	stepInitAgain
	stepLink
	stepReady
	stepExpired
	stepConflict
	stepNotSent
	stepUnknown
	stepDone
	stepNotBroadcast
)

// Prepare builds an invoice and does not sign it.
// A stored req_id is never sent to init again. A nil validUntil repeats get*Link once.
func (c *Client) Prepare(ctx context.Context, order Order) (pp *PreparedPayment, err error) {
	start := c.now()
	var rec *Record
	defer func() {
		c.observe("prepare", order, rec, err, start)
	}()
	pp, rec, err = c.prepare(ctx, order)
	return pp, err
}

// PrepareStars reads ShowSender from the client. The default is true.
func (c *Client) PrepareStars(ctx context.Context, username string, stars int, key IdempotencyKey) (*PreparedPayment, error) {
	return c.Prepare(ctx, Order{Product: ProductStars, Username: username, Stars: stars, ShowSender: c.showSender, IdempotencyKey: key})
}

// PreparePremium reads ShowSender from the client. Months must be 3, 6, or 12.
func (c *Client) PreparePremium(ctx context.Context, username string, months int, key IdempotencyKey) (*PreparedPayment, error) {
	return c.Prepare(ctx, Order{Product: ProductPremium, Username: username, Months: months, ShowSender: c.showSender, IdempotencyKey: key})
}

// FindStarsRecipient looks up a username and does not write the journal.
func (c *Client) FindStarsRecipient(ctx context.Context, username string) (Recipient, error) {
	return c.find(ctx, protocol.Stars, username, 0)
}

// FindPremiumRecipient looks up a username and does not write the journal.
func (c *Client) FindPremiumRecipient(ctx context.Context, username string) (Recipient, error) {
	return c.find(ctx, protocol.Premium, username, 0)
}

func (c *Client) find(ctx context.Context, flow protocol.Flow, username string, months int) (Recipient, error) {
	name, err := NormalizeUsername(username)
	if err != nil {
		return Recipient{}, err
	}
	if c.requireCookies && !c.cookies.ready() {
		return Recipient{}, ErrCookiesRequired
	}
	ctx, cancel := c.budget(ctx)
	defer cancel()
	return c.search(ctx, flow, name, months)
}

func (c *Client) prepare(ctx context.Context, order Order) (*PreparedPayment, *Record, error) {
	order, err := c.normalize(order)
	if err != nil {
		return nil, nil, err
	}
	flow, err := c.flow(order.Product)
	if err != nil {
		return nil, nil, err
	}
	if _, err := c.account(ctx); err != nil {
		return nil, nil, err
	}
	ctx, cancel := c.budget(ctx)
	defer cancel()

	rel, err := c.lockKey(ctx, order.IdempotencyKey)
	if err != nil {
		return nil, nil, err
	}
	rec, err := c.journal.Load(ctx, order.IdempotencyKey)
	if err != nil && !errorsIsNotFound(err) {
		rel()
		return nil, nil, err
	}
	if errorsIsNotFound(err) {
		acc, _ := c.account(ctx)
		rec = c.newRecord(order, acc.Address)
		err = c.journal.Create(ctx, rec)
		if err != nil && !errorsIsExists(err) {
			rel()
			return nil, nil, err
		}
		if errorsIsExists(err) {
			rec, err = c.journal.Load(ctx, order.IdempotencyKey)
			if err != nil {
				rel()
				return nil, nil, err
			}
		} else {
			rel()
			got, err := c.buy(ctx, flow, order, true)
			return got, recordPtr(got), err
		}
	}
	step := c.classify(rec, order)
	switch step {
	case stepConflict:
		rel()
		return nil, &rec, ErrIdempotencyConflict
	case stepInProgress:
		rel()
		return nil, &rec, ErrPrepareInProgress
	case stepStuck:
		rel()
		return nil, &rec, ErrPrepareStuck
	case stepExpired:
		rel()
		return nil, &rec, &InvoiceExpiredError{Key: order.IdempotencyKey}
	case stepNotSent:
		rel()
		return nil, &rec, &NotSentError{Key: rec.Key, MsgHash: rec.MsgHash}
	case stepUnknown:
		rel()
		return nil, &rec, &OutcomeUnknownError{Key: rec.Key, MsgHash: rec.MsgHash}
	case stepNotBroadcast:
		rel()
		return nil, &rec, &NotBroadcastError{Payment: toPayment(rec)}
	case stepDone, stepReady, stepSign:
		rel()
		pp := preparedFrom(rec)
		return pp, &rec, nil
	case stepLink:
		rel()
		got, err := c.buy(ctx, flow, order, false)
		return got, recordPtr(got), err
	case stepInitAgain:
		rec.InitAttempts = 2
		rec.UpdatedAt = c.now()
		if _, err := c.save(ctx, rec); err != nil {
			rel()
			return nil, &rec, err
		}
		rel()
		got, err := c.buy(ctx, flow, order, true)
		return got, recordPtr(got), err
	default:
		rel()
		return nil, &rec, ErrJournal
	}
}

func (c *Client) normalize(order Order) (Order, error) {
	if c.requireCookies && !c.cookies.linkReady() {
		return Order{}, ErrCookiesRequired
	}
	if err := validateOrderShape(order); err != nil {
		return Order{}, err
	}
	name, err := NormalizeUsername(order.Username)
	if err != nil {
		return Order{}, err
	}
	order.Username = name
	return order, nil
}

func (c *Client) newRecord(order Order, sender string) Record {
	now := c.now()
	rec := Record{
		Schema:       1,
		Version:      1,
		Key:          order.IdempotencyKey,
		Product:      order.Product,
		Username:     order.Username,
		Quantity:     order.quantity(),
		ShowSender:   order.ShowSender,
		State:        StatePreparing,
		SenderRaw:    sender,
		InitAttempts: 1,
		CreatedAt:    now,
		UpdatedAt:    now,
		Messages:     []OutgoingMessage{},
	}
	if c.donation != nil {
		rec.DonationAddress = c.donation.Address
		rec.DonationBasisPoints = c.donation.BasisPoints
		rec.DonationFixedNano = c.donation.FixedNanoTON
	}
	return rec
}

func (c *Client) classify(rec Record, order Order) nextStep {
	acc, _ := c.account(context.Background())
	if !c.sameOrder(rec, order, acc.Address) {
		return stepConflict
	}
	switch rec.State {
	case StatePreparing:
		if rec.ReqID == "" {
			if rec.InitAttempts >= 2 {
				return stepStuck
			}
			if c.now().Sub(rec.UpdatedAt) < prepareRetryAfter {
				return stepInProgress
			}
			return stepInitAgain
		}
		if len(rec.Messages) == 0 || rec.ValidUntil == nil {
			return stepLink
		}
		return c.preparedStep(rec)
	case StatePrepared:
		if len(rec.Messages) == 0 || rec.ValidUntil == nil {
			return stepLink
		}
		return c.preparedStep(rec)
	case StateSigned:
		if rec.SendAttempted {
			return stepUnknown
		}
		return stepNotSent
	case StateUnknown:
		return stepUnknown
	case StateBroadcast, StateConfirmed, StateSettled:
		return stepDone
	case StateNotBroadcast:
		return stepNotBroadcast
	default:
		return stepConflict
	}
}

func (c *Client) preparedStep(rec Record) nextStep {
	if rec.ValidUntil == nil {
		return stepLink
	}
	if rec.BOC != "" {
		return stepNotSent
	}
	if rec.ValidUntil.After(c.now()) {
		return stepSign
	}
	return stepExpired
}

// buy talks to Fragment. full is state, search, and one init. Otherwise it is only get*Link.
func (c *Client) buy(ctx context.Context, flow protocol.Flow, order Order, full bool) (*PreparedPayment, error) {
	rel, err := c.lockKey(ctx, order.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	rec, err := c.journal.Load(ctx, order.IdempotencyKey)
	rel()
	if err != nil {
		return nil, err
	}
	if rec.ReqID != "" {
		full = false
	}
	if full {
		if err := c.postState(ctx, flow); err != nil {
			return nil, err
		}
		who, err := c.search(ctx, flow, order.Username, order.Months)
		if err != nil {
			return nil, err
		}
		reqID, principal, err := c.postInit(ctx, flow, order, who.Token)
		if err != nil {
			return nil, err
		}
		if reqID == "" {
			return nil, ErrNoRequestID
		}
		rel, err = c.lockKey(ctx, order.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		rec, err = c.journal.Load(ctx, order.IdempotencyKey)
		if err != nil {
			rel()
			return nil, err
		}
		if rec.ReqID != "" && rec.ReqID != reqID {
			rel()
			return nil, ErrReqIDConflict
		}
		acc, err := c.account(ctx)
		if err != nil {
			rel()
			return nil, err
		}
		accountJSON, err := marshalAccount(acc)
		if err != nil {
			rel()
			return nil, err
		}
		rec.ReqID = reqID
		rec.RecipientToken = who.Token
		rec.RecipientName = who.Name
		rec.AccountJSON = accountJSON
		rec.SenderRaw = acc.Address
		rec.Breakdown.PaymentNanoTON = principal
		rec.UpdatedAt = c.now()
		rec, err = c.save(ctx, rec)
		rel()
		if err != nil {
			return nil, err
		}
	}
	return c.fetchLink(ctx, flow, order)
}

func (c *Client) fetchLink(ctx context.Context, flow protocol.Flow, order Order) (*PreparedPayment, error) {
	rel, err := c.lockKey(ctx, order.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	rec, err := c.journal.Load(ctx, order.IdempotencyKey)
	if err != nil {
		rel()
		return nil, err
	}
	if !c.sameOrder(rec, order, rec.SenderRaw) {
		rel()
		return nil, ErrIdempotencyConflict
	}
	if rec.ReqID == "" {
		rel()
		return nil, ErrNoRequestID
	}
	if len(rec.Messages) > 0 && rec.ValidUntil != nil && rec.ValidUntil.After(c.now()) && rec.BOC == "" {
		rel()
		return preparedFrom(rec), nil
	}
	accountJSON := rec.AccountJSON
	reqID := rec.ReqID
	principal := rec.Breakdown.PaymentNanoTON
	rel()

	acc, err := c.account(ctx)
	if err != nil {
		return nil, err
	}
	if accountJSON == "" {
		accountJSON, err = marshalAccount(acc)
		if err != nil {
			return nil, err
		}
	}
	fields := url.Values{}
	fields.Set("account", accountJSON)
	fields.Set("device", protocol.DeviceFingerprint)
	fields.Set("transaction", "1")
	fields.Set("id", reqID)
	if order.ShowSender {
		fields.Set("show_sender", "1")
	} else {
		fields.Set("show_sender", "0")
	}
	reply, err := c.transport.Call(ctx, Call{Page: flow.Page, Method: flow.LinkMethod, Fields: fields})
	if err != nil {
		return nil, err
	}
	if err := bodyError(flow.LinkMethod, reply); err != nil {
		return nil, err
	}
	inv, err := protocol.ParseLink(reply.Body)
	if err != nil {
		if err == protocol.ErrUnexpected {
			return nil, ErrUnexpectedPaymentFlow
		}
		return nil, ErrInvoiceMismatch
	}
	if inv.FromRaw != acc.Address {
		return nil, ErrInvoiceMismatch
	}
	pp, outs, err := c.quoteInvoice(order, principal, inv)
	if err != nil {
		return nil, err
	}
	until := inv.ValidUntil
	rel, err = c.lockKey(ctx, order.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	defer rel()
	rec, err = c.journal.Load(ctx, order.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if rec.ReqID != reqID {
		return nil, ErrReqIDConflict
	}
	rec.State = StatePrepared
	rec.AccountJSON = accountJSON
	rec.SenderRaw = acc.Address
	rec.Messages = outs
	rec.Breakdown = pp.Breakdown
	rec.ValidUntil = &until
	rec.UpdatedAt = c.now()
	rec, err = c.save(ctx, rec)
	if err != nil {
		return nil, err
	}
	return preparedFrom(rec), nil
}

func (c *Client) quoteInvoice(order Order, principal int64, inv protocol.Invoice) (*PreparedPayment, []OutgoingMessage, error) {
	if principal <= 0 {
		return nil, nil, ErrInvoiceMismatch
	}
	if len(inv.Messages) < 1 || len(inv.Messages) > c.maxMessages {
		return nil, nil, ErrTooManyMessages
	}
	lines := make([]MessageLine, 0, len(inv.Messages)+1)
	outs := make([]OutgoingMessage, 0, len(inv.Messages)+1)
	var frag int64
	for _, m := range inv.Messages {
		if err := rejectFragment(m.Address); err != nil {
			return nil, nil, err
		}
		lines = append(lines, MessageLine{Purpose: PurposeFragment, Address: m.Address, AmountNano: m.AmountNano})
		outs = append(outs, OutgoingMessage{Address: m.Address, AmountNano: m.AmountNano, Payload: m.Payload, StateInit: m.StateInit})
		var err error
		frag, err = sumNano(frag, m.AmountNano)
		if err != nil {
			return nil, nil, err
		}
	}
	addr := ""
	basis := 0
	if c.donation != nil {
		amount := c.donation.FixedNanoTON
		basis = c.donation.BasisPoints
		if basis > 0 {
			var err error
			amount, err = PercentNano(principal, basis)
			if err != nil {
				return nil, nil, err
			}
		}
		if amount <= 0 {
			return nil, nil, ErrInvoiceMismatch
		}
		if err := rejectDonation(c.donation.Address); err != nil {
			return nil, nil, err
		}
		addr = c.donation.Address
		lines = append(lines, MessageLine{Purpose: PurposeDonation, Address: addr, AmountNano: amount})
		outs = append(outs, OutgoingMessage{Address: addr, AmountNano: amount})
	}
	bd, err := quote(order.Product, order.quantity(), principal, c.gasReserve, lines, addr, basis)
	if err != nil {
		return nil, nil, err
	}
	bd.ValidUntil = inv.ValidUntil
	return &PreparedPayment{Breakdown: bd}, outs, nil
}

func (c *Client) postState(ctx context.Context, flow protocol.Flow) error {
	fields := url.Values{}
	fields.Set("mode", "new")
	fields.Set("lv", "false")
	fields.Set("dh", strconv.FormatInt(c.now().UnixNano(), 10))
	reply, err := c.transport.Call(ctx, Call{Page: flow.Page, Method: flow.StateMethod, Fields: fields})
	if err != nil {
		return err
	}
	return bodyError(flow.StateMethod, reply)
}

func (c *Client) search(ctx context.Context, flow protocol.Flow, username string, months int) (Recipient, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, searchRetryGap); err != nil {
				return Recipient{}, err
			}
		}
		fields := url.Values{}
		fields.Set("query", username)
		if flow.Product == string(ProductPremium) {
			if months > 0 {
				fields.Set("months", strconv.Itoa(months))
			} else {
				fields.Set("months", "")
			}
		} else {
			fields.Set("quantity", "")
		}
		reply, err := c.transport.Call(ctx, Call{Page: flow.Page, Method: flow.SearchMethod, Fields: fields})
		if err != nil {
			last = err
			continue
		}
		if reply.HTTPStatus == 429 {
			last = &APIError{Method: flow.SearchMethod, HTTPStatus: 429, FragmentMessage: "429"}
			continue
		}
		if err := bodyError(flow.SearchMethod, reply); err != nil {
			return Recipient{}, err
		}
		return parseRecipient(reply.Body)
	}
	if last == nil {
		last = ErrUserNotFound
	}
	return Recipient{}, last
}

func (c *Client) postInit(ctx context.Context, flow protocol.Flow, order Order, recipient string) (string, int64, error) {
	fields := url.Values{}
	fields.Set("recipient", recipient)
	fields.Set("payment_method", "ton")
	if flow.Product == string(ProductPremium) {
		fields.Set("months", strconv.Itoa(order.Months))
	} else {
		fields.Set("quantity", strconv.Itoa(order.Stars))
	}
	reply, err := c.transport.Call(ctx, Call{Page: flow.Page, Method: flow.InitMethod, Fields: fields})
	if err != nil {
		return "", 0, err
	}
	if err := bodyError(flow.InitMethod, reply); err != nil {
		return "", 0, err
	}
	var raw struct {
		ReqID  string          `json:"req_id"`
		Amount json.RawMessage `json:"amount"`
	}
	if err := json.Unmarshal(reply.Body, &raw); err != nil {
		return "", 0, ErrNoRequestID
	}
	if strings.TrimSpace(raw.ReqID) == "" {
		return "", 0, ErrNoRequestID
	}
	principal, err := protocol.ParseTONAmount(raw.Amount)
	if err != nil || principal <= 0 {
		return "", 0, ErrInvoiceMismatch
	}
	return raw.ReqID, principal, nil
}

func parseRecipient(body []byte) (Recipient, error) {
	var raw struct {
		Found *struct {
			Recipient string `json:"recipient"`
			Name      string `json:"name"`
			Photo     string `json:"photo"`
			Myself    bool   `json:"myself"`
		} `json:"found"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.Found == nil || raw.Found.Recipient == "" {
		return Recipient{}, ErrUserNotFound
	}
	return Recipient{Token: raw.Found.Recipient, Name: raw.Found.Name, PhotoURL: raw.Found.Photo, Myself: raw.Found.Myself}, nil
}

func bodyError(method string, reply Reply) error {
	if reply.HTTPStatus >= 500 || reply.HTTPStatus == 0 {
		return &APIError{Method: method, HTTPStatus: reply.HTTPStatus, FragmentMessage: "unavailable"}
	}
	text := protocol.ErrorText(reply.Body)
	if text == "" && reply.HTTPStatus >= 400 {
		text = strings.TrimSpace(string(reply.Body))
	}
	if text == "" {
		return nil
	}
	low := strings.ToLower(text)
	if strings.Contains(low, "already subscribed") {
		return ErrAlreadySubscribed
	}
	if strings.Contains(low, "not a user") {
		return ErrUserNotFound
	}
	msg := Scrub(text)
	if len(msg) > 256 {
		msg = msg[:256]
	}
	return &APIError{Method: method, HTTPStatus: reply.HTTPStatus, FragmentMessage: msg}
}

func marshalAccount(a Account) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	err := enc.Encode(struct {
		Address         string `json:"address"`
		Chain           string `json:"chain"`
		WalletStateInit string `json:"walletStateInit"`
		PublicKey       string `json:"publicKey"`
	}{a.Address, a.Chain, a.WalletStateInit, a.PublicKey})
	if err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

func preparedFrom(rec Record) *PreparedPayment {
	until := rec.ValidUntil
	var who Recipient
	who.Token = rec.RecipientToken
	who.Name = rec.RecipientName
	bd := rec.Breakdown
	if until != nil {
		bd.ValidUntil = *until
	}
	return &PreparedPayment{
		Key:       rec.Key,
		State:     rec.State,
		ReqID:     rec.ReqID,
		Recipient: who,
		Breakdown: bd,
		Transaction: PreparedTransaction{
			SenderRaw:  rec.SenderRaw,
			ValidUntil: until,
			Messages:   append([]OutgoingMessage(nil), rec.Messages...),
		},
	}
}

func recordPtr(pp *PreparedPayment) *Record {
	if pp == nil {
		return nil
	}
	return &Record{Key: pp.Key, State: pp.State, ReqID: pp.ReqID, Breakdown: pp.Breakdown}
}

func errorsIsNotFound(err error) bool { return err != nil && CodeOf(err) == ErrNotFound.code }
func errorsIsExists(err error) bool   { return err != nil && CodeOf(err) == errJournalExists.code }
