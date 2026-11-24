package fragment

import (
	"context"
	"errors"

	"github.com/andhash/go-fragment/wallet"
)

// errRetryPay asks Pay to read the journal again after a link repair.
var errRetryPay = errors.New("retry pay")

// Pay signs and broadcasts a prepared invoice.
//
// A nil error is not delivery. nil with StateBroadcast means the broadcaster
// accepted the message. nil with StateConfirmed or StateSettled means the
// stored row was already closed. Pay does not call confirmReq and does not
// wait for ConfirmTimeout.
//
// Retry the same idempotency key for wallet_busy, prepare_in_progress,
// insufficient_balance, balance_unavailable, seqno_unavailable, spend_limit,
// treasury_unpinned, and journal while the BOC is not stored yet.
// invoice_expired and prepare_stuck need a new key.
// outcome_unknown, not_sent, idempotency_conflict, and destination_rejected
// need a review and not a refund. Resume is the only call that sends a stored
// BOC, and only when send_attempted is still false.
func (c *Client) Pay(ctx context.Context, order Order) (pay *Payment, err error) {
	start := c.now()
	var rec *Record
	defer func() {
		c.observe("pay", order, rec, err, start)
	}()
	for depth := 0; depth < 4; depth++ {
		pay, rec, err = c.payOnce(ctx, order)
		if errors.Is(err, errRetryPay) {
			err = nil
			continue
		}
		return pay, err
	}
	err = ErrPrepareStuck
	return nil, err
}

// PayStars reads ShowSender from the client. The default is true.
func (c *Client) PayStars(ctx context.Context, username string, stars int, key IdempotencyKey) (*Payment, error) {
	return c.Pay(ctx, Order{Product: ProductStars, Username: username, Stars: stars, ShowSender: c.showSender, IdempotencyKey: key})
}

// PayPremium reads ShowSender from the client. Months must be 3, 6, or 12.
func (c *Client) PayPremium(ctx context.Context, username string, months int, key IdempotencyKey) (*Payment, error) {
	return c.Pay(ctx, Order{Product: ProductPremium, Username: username, Months: months, ShowSender: c.showSender, IdempotencyKey: key})
}

// Balance is Broadcaster.BalanceNano for the signer. It does not sign or send.
// A network failure is ErrBalanceUnavailable. Pay still checks the balance before Sign.
func (c *Client) Balance(ctx context.Context) (int64, error) {
	acc, err := c.account(ctx)
	if err != nil {
		return 0, err
	}
	n, err := c.broadcaster.BalanceNano(ctx, acc.Address)
	if err != nil {
		return 0, withCause(ErrBalanceUnavailable, err)
	}
	return n, nil
}

func (c *Client) payOnce(ctx context.Context, order Order) (*Payment, *Record, error) {
	order, err := c.normalize(order)
	if err != nil {
		return nil, nil, err
	}
	if _, err := c.account(ctx); err != nil {
		return nil, nil, err
	}
	relKey, err := c.lockKey(ctx, order.IdempotencyKey)
	if err != nil {
		return nil, nil, err
	}
	rec, err := c.journal.Load(ctx, order.IdempotencyKey)
	if errorsIsNotFound(err) {
		relKey()
		if _, _, err := c.prepare(ctx, order); err != nil {
			return nil, nil, err
		}
		return nil, nil, errRetryPay
	}
	if err != nil {
		relKey()
		return nil, nil, err
	}
	switch c.classify(rec, order) {
	case stepSign:
		pay, err := c.signAndSend(ctx, order, rec, relKey)
		if pay != nil {
			rec.State = pay.State
			rec.MsgHash = pay.MsgHash
			rec.ReqID = pay.ReqID
		}
		return pay, &rec, err
	case stepLink, stepInitAgain:
		relKey()
		if _, _, err := c.prepare(ctx, order); err != nil {
			return nil, &rec, err
		}
		return nil, &rec, errRetryPay
	case stepInProgress:
		relKey()
		return nil, &rec, ErrPrepareInProgress
	case stepStuck:
		relKey()
		return nil, &rec, ErrPrepareStuck
	case stepConflict:
		relKey()
		return nil, &rec, ErrIdempotencyConflict
	case stepExpired:
		relKey()
		return nil, &rec, &InvoiceExpiredError{Key: rec.Key}
	case stepNotSent:
		relKey()
		return nil, &rec, &NotSentError{Key: rec.Key, MsgHash: rec.MsgHash}
	case stepUnknown:
		relKey()
		return nil, &rec, &OutcomeUnknownError{Key: rec.Key, MsgHash: rec.MsgHash}
	case stepDone:
		relKey()
		return toPayment(rec), &rec, nil
	case stepNotBroadcast:
		relKey()
		p := toPayment(rec)
		return p, &rec, &NotBroadcastError{Payment: p}
	default:
		relKey()
		return nil, &rec, ErrJournal
	}
}

// signAndSend holds the key lock and takes the address lock second.
// Every return releases the address lock before the key lock.
func (c *Client) signAndSend(ctx context.Context, order Order, rec Record, relKey func()) (*Payment, error) {
	acc, err := c.account(ctx)
	if err != nil {
		relKey()
		return nil, err
	}
	relAddr, err := c.lockAddr(ctx, acc.Address)
	if err != nil {
		relKey()
		return nil, err
	}
	release := func() {
		if relAddr != nil {
			relAddr()
			relAddr = nil
		}
		if relKey != nil {
			relKey()
			relKey = nil
		}
	}
	rec, err = c.journal.Load(ctx, order.IdempotencyKey)
	if err != nil {
		release()
		return nil, err
	}
	if len(rec.Messages) == 0 || rec.ValidUntil == nil {
		release()
		return nil, errRetryPay
	}
	if rec.BOC == "" && !rec.ValidUntil.After(c.now()) {
		release()
		return nil, &InvoiceExpiredError{Key: rec.Key}
	}
	if !c.sameOrder(rec, order, acc.Address) {
		release()
		return nil, ErrIdempotencyConflict
	}
	rows, err := c.journal.List(ctx)
	if err != nil {
		release()
		return nil, err
	}
	for _, row := range rows {
		if row.Key == rec.Key || row.SenderRaw != acc.Address {
			continue
		}
		busy := row.State == StateSigned || row.State == StateUnknown || (row.State == StateBroadcast && row.SeqnoAfter == nil)
		if busy {
			release()
			return nil, ErrWalletBusy
		}
	}
	if err := c.pin(rec); err != nil {
		release()
		return nil, err
	}
	if !c.spendSet || rec.Breakdown.RequiredNanoTON > c.spendLimit {
		release()
		return nil, ErrSpendLimit
	}
	bal, err := c.broadcaster.BalanceNano(ctx, acc.Address)
	if err != nil {
		release()
		return nil, withCause(ErrBalanceUnavailable, err)
	}
	if bal < rec.Breakdown.RequiredNanoTON {
		release()
		return nil, ErrInsufficientBalance
	}
	seqno, err := c.broadcaster.Seqno(ctx, acc.Address)
	if err != nil {
		release()
		return nil, withCause(ErrSeqnoUnavailable, err)
	}
	signed, err := c.signer.Sign(ctx, PreparedTransaction{
		SenderRaw:  acc.Address,
		ValidUntil: rec.ValidUntil,
		Messages:   rec.Messages,
	}, seqno)
	if err != nil {
		release()
		return nil, err
	}
	if err := c.matchBOC(signed, rec.Messages, seqno); err != nil {
		release()
		return nil, err
	}
	seq := seqno
	rec.State = StateSigned
	rec.BOC = signed.BOC
	rec.MsgHash = signed.MsgHash
	rec.SeqnoBefore = &seq
	rec.SendAttempted = false
	rec.UpdatedAt = c.now()
	rec, err = c.save(ctx, rec)
	if err != nil {
		release()
		return nil, err
	}
	rec.SendAttempted = true
	rec.UpdatedAt = c.now()
	rec, err = c.save(ctx, rec)
	if err != nil {
		release()
		return nil, err
	}
	if err := c.broadcaster.Send(ctx, signed); err != nil {
		rec.State = StateUnknown
		rec.UpdatedAt = c.now()
		_, _ = c.save(ctx, rec)
		release()
		return nil, &OutcomeUnknownError{Key: rec.Key, MsgHash: rec.MsgHash, Cause: err}
	}
	rec.State = StateBroadcast
	rec.UpdatedAt = c.now()
	saved, err := c.save(ctx, rec)
	release()
	if err != nil {
		return nil, &OutcomeUnknownError{Key: rec.Key, MsgHash: rec.MsgHash, Cause: err}
	}
	return toPayment(saved), nil
}

func (c *Client) pin(rec Record) error {
	if len(rec.Breakdown.Messages) != len(rec.Messages) {
		return ErrBreakdownMismatch
	}
	pinned := false
	for i, line := range rec.Breakdown.Messages {
		if line.Purpose != PurposeFragment {
			if err := rejectDonation(rec.Messages[i].Address); err != nil {
				return err
			}
			continue
		}
		pinned = true
		if err := rejectFragment(rec.Messages[i].Address); err != nil {
			return err
		}
		if line.Address != rec.Messages[i].Address || line.AmountNano != rec.Messages[i].AmountNano {
			return ErrBreakdownMismatch
		}
		if !c.allowUnpinned {
			if _, ok := c.treasury[rec.Messages[i].Address]; !ok {
				if len(c.treasury) == 0 {
					return ErrTreasuryUnpinned
				}
				return ErrDestinationRejected
			}
		}
	}
	if !pinned {
		return ErrInvoiceMismatch
	}
	if !c.allowUnpinned && len(c.treasury) == 0 {
		return ErrTreasuryUnpinned
	}
	return nil
}

func (c *Client) matchBOC(signed SignedMessage, messages []OutgoingMessage, seqno uint32) error {
	if signed.BOC == "" || signed.MsgHash == "" || signed.Seqno != seqno {
		return ErrBreakdownMismatch
	}
	outs, err := wallet.ParseOutputs(signed.BOC)
	if err != nil || len(outs) != len(messages) {
		return ErrBreakdownMismatch
	}
	for i, m := range messages {
		raw, err := NormalizeAddress(m.Address)
		if err != nil || outs[i].Address != raw || outs[i].AmountNano != m.AmountNano {
			return ErrBreakdownMismatch
		}
	}
	return nil
}
