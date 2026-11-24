package fragment

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"

	"github.com/andhash/go-fragment/internal/protocol"
)

// Get returns the stored payment and does not call the network.
func (c *Client) Get(ctx context.Context, key IdempotencyKey) (*Payment, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	rec, err := c.journal.Load(ctx, key)
	if err != nil {
		return nil, err
	}
	return toPayment(rec), nil
}

// List returns stored payments. It does not acquire key locks.
func (c *Client) List(ctx context.Context) ([]Payment, error) {
	rows, err := c.journal.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Payment, 0, len(rows))
	for _, rec := range rows {
		out = append(out, *toPayment(rec))
	}
	return out, nil
}

// Reconcile posts confirmReq for a broadcast payment and then polls Fragment.
// It does not call Broadcaster.Send and it does not call init.
// A transport failure on confirmReq is ErrConfirmUnavailable and leaves the row broadcast.
// A poll that does not see done before ConfirmTimeout is ConfirmationTimeoutError.
// confirmed and settled return the row and nil and do not post again.
func (c *Client) Reconcile(ctx context.Context, key IdempotencyKey) (pay *Payment, err error) {
	start := c.now()
	var rec Record
	defer func() {
		op := "reconcile"
		if rec.State == StateConfirmed {
			op = "confirm"
		}
		c.observe(op, Order{Product: rec.Product, IdempotencyKey: key, Stars: rec.Quantity}, &rec, err, start)
	}()
	if err = key.Validate(); err != nil {
		return nil, err
	}
	rec, err = c.loadKey(ctx, key)
	if err != nil {
		return nil, err
	}
	switch rec.State {
	case StateConfirmed, StateSettled:
		return toPayment(rec), nil
	case StateNotBroadcast:
		p := toPayment(rec)
		return p, &NotBroadcastError{Payment: p}
	case StateSigned:
		if !rec.SendAttempted {
			return toPayment(rec), &NotSentError{Key: rec.Key, MsgHash: rec.MsgHash}
		}
		_ = c.noteSeqno(ctx, &rec)
		return toPayment(rec), &OutcomeUnknownError{Key: rec.Key, MsgHash: rec.MsgHash}
	case StateUnknown:
		_ = c.noteSeqno(ctx, &rec)
		return toPayment(rec), &OutcomeUnknownError{Key: rec.Key, MsgHash: rec.MsgHash}
	case StateBroadcast:
		_ = c.noteSeqno(ctx, &rec)
		return c.confirm(ctx, rec)
	default:
		return toPayment(rec), coded("reconcile_state", "payment is not broadcast")
	}
}

// Resolve writes settled or not_broadcast.
// settled requires LookupFound in this call. not_broadcast requires a non-nil
// validUntil in the past and LookupNotFound. Otherwise the file is unchanged.
func (c *Client) Resolve(ctx context.Context, key IdempotencyKey, outcome ResolveOutcome, evidence string) (*Payment, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if evidence == "" || (outcome != ResolveSettled && outcome != ResolveNotBroadcast) {
		return nil, ErrResolveRefused
	}
	rec, err := c.loadKey(ctx, key)
	if err != nil {
		return nil, err
	}
	if rec.State == State(outcome) {
		return toPayment(rec), nil
	}
	if rec.State == StateConfirmed || rec.State == StateSettled || rec.State == StateNotBroadcast {
		return nil, ErrResolveRefused
	}
	status := LookupUnavailable
	if rec.MsgHash != "" {
		var lerr error
		status, lerr = c.broadcaster.Lookup(ctx, rec.MsgHash)
		if lerr != nil {
			status = LookupUnavailable
		}
	}
	rel, err := c.lockKey(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rel()
	rec, err = c.journal.Load(ctx, key)
	if err != nil {
		return nil, err
	}
	switch outcome {
	case ResolveSettled:
		if (rec.State != StateUnknown && rec.State != StateBroadcast) || status != LookupFound {
			return nil, ErrResolveRefused
		}
		rec.State = StateSettled
	case ResolveNotBroadcast:
		if rec.ValidUntil == nil || rec.ValidUntil.After(c.now()) || status != LookupNotFound {
			return nil, ErrResolveRefused
		}
		if rec.State != StateSigned && rec.State != StateUnknown && rec.State != StateBroadcast {
			return nil, ErrResolveRefused
		}
		rec.State = StateNotBroadcast
	}
	rec.ResolutionEvidence = evidence
	rec.UpdatedAt = c.now()
	rec, err = c.save(ctx, rec)
	if err != nil {
		return nil, err
	}
	return toPayment(rec), nil
}

// Resume sends a signed BOC that was never sent. Pay does not call it.
// send_attempted already true is OutcomeUnknownError. LookupUnavailable is
// ErrChainUnavailable and leaves the row signed. A nil or past validUntil,
// or LookupFound, is ErrResumeRefused. A past validUntil with LookupNotFound
// is closed with Resolve(not_broadcast), not with a nil error from Resume.
func (c *Client) Resume(ctx context.Context, key IdempotencyKey) (*Payment, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	relKey, err := c.lockKey(ctx, key)
	if err != nil {
		return nil, err
	}
	rec, err := c.journal.Load(ctx, key)
	if err != nil {
		relKey()
		return nil, err
	}
	if rec.SendAttempted {
		relKey()
		return nil, &OutcomeUnknownError{Key: rec.Key, MsgHash: rec.MsgHash}
	}
	relAddr, err := c.lockAddr(ctx, rec.SenderRaw)
	if err != nil {
		relKey()
		return nil, err
	}
	release := func() {
		relAddr()
		relKey()
	}
	rec, err = c.journal.Load(ctx, key)
	if err != nil {
		release()
		return nil, err
	}
	if rec.SendAttempted {
		release()
		return nil, &OutcomeUnknownError{Key: rec.Key, MsgHash: rec.MsgHash}
	}
	if rec.State != StateSigned || rec.BOC == "" {
		release()
		return nil, ErrResumeRefused
	}
	status, lerr := c.broadcaster.Lookup(ctx, rec.MsgHash)
	if lerr != nil || status == LookupUnavailable {
		release()
		return nil, withCause(ErrChainUnavailable, lerr)
	}
	future := rec.ValidUntil != nil && rec.ValidUntil.After(c.now())
	if !future || status == LookupFound {
		release()
		return nil, ErrResumeRefused
	}
	if status != LookupNotFound {
		release()
		return nil, ErrResumeRefused
	}
	signed := SignedMessage{MsgHash: rec.MsgHash, BOC: rec.BOC, Seqno: seqOrZero(rec.SeqnoBefore)}
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
	rec, err = c.save(ctx, rec)
	release()
	if err != nil {
		return nil, &OutcomeUnknownError{Key: rec.Key, MsgHash: rec.MsgHash, Cause: err}
	}
	return toPayment(rec), nil
}

func (c *Client) confirm(ctx context.Context, rec Record) (*Payment, error) {
	flow, err := c.flow(rec.Product)
	if err != nil {
		return toPayment(rec), err
	}
	if rec.ReqID == "" || rec.AccountJSON == "" || rec.BOC == "" {
		return toPayment(rec), ErrConfirmUnavailable
	}
	fields := url.Values{}
	fields.Set("id", rec.ReqID)
	fields.Set("account", rec.AccountJSON)
	fields.Set("device", protocol.DeviceFingerprint)
	fields.Set("boc", rec.BOC)
	ctxPost, cancel := context.WithTimeout(ctx, c.prepareTimeout)
	reply, err := c.transport.Call(ctxPost, Call{Page: flow.Page, Method: flow.ConfirmMethod, Fields: fields})
	cancel()
	if err != nil || reply.HTTPStatus >= 500 || reply.HTTPStatus == 0 || reply.HTTPStatus >= 400 {
		cause := err
		if cause == nil {
			cause = &APIError{Method: flow.ConfirmMethod, HTTPStatus: reply.HTTPStatus, FragmentMessage: "confirm failed"}
		}
		return toPayment(rec), withCause(ErrConfirmUnavailable, cause)
	}
	deadline := c.now().Add(c.confirmTimeout)
	mode := "new"
	for {
		if !c.now().Before(deadline) {
			return toPayment(rec), &ConfirmationTimeoutError{Payment: toPayment(rec)}
		}
		poll := url.Values{}
		poll.Set("id", rec.ReqID)
		poll.Set("mode", mode)
		poll.Set("lv", "false")
		poll.Set("dh", strconv.FormatInt(c.now().UnixNano(), 10))
		ctxPoll, cancelPoll := context.WithTimeout(ctx, c.prepareTimeout)
		reply, err = c.transport.Call(ctxPoll, Call{Page: flow.Page, Method: flow.StateMethod, Fields: poll})
		cancelPoll()
		if err == nil && reply.HTTPStatus == 200 {
			done, next := confirmDone(reply.Body, rec.ReqID)
			if next != "" {
				mode = next
			}
			if done {
				rel, lerr := c.lockKey(ctx, rec.Key)
				if lerr != nil {
					return toPayment(rec), lerr
				}
				cur, lerr := c.journal.Load(ctx, rec.Key)
				if lerr != nil {
					rel()
					return toPayment(rec), lerr
				}
				if cur.State == StateConfirmed || cur.State == StateSettled {
					rel()
					return toPayment(cur), nil
				}
				cur.State = StateConfirmed
				cur.UpdatedAt = c.now()
				cur, lerr = c.save(ctx, cur)
				rel()
				if lerr != nil {
					return toPayment(rec), lerr
				}
				return toPayment(cur), nil
			}
		}
		if err := c.sleep(ctx, confirmPollGap); err != nil {
			return toPayment(rec), &ConfirmationTimeoutError{Payment: toPayment(rec)}
		}
	}
}

func confirmDone(body []byte, reqID string) (bool, string) {
	var raw struct {
		Mode       string          `json:"mode"`
		NeedUpdate json.RawMessage `json:"need_update"`
		ReqID      string          `json:"req_id"`
		ID         string          `json:"id"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return false, ""
	}
	matched := raw.ReqID == reqID || raw.ID == reqID
	done := raw.Mode == "done" && bytes.Equal(bytes.TrimSpace(raw.NeedUpdate), []byte("false")) && matched
	return done, raw.Mode
}

// noteSeqno fills seqno_after without the address lock.
// LookupFound sets it immediately. Otherwise Seqno must be greater than seqno_before.
// A Seqno error leaves the field nil and is not NotFound and not ErrSeqnoUnavailable.
func (c *Client) noteSeqno(ctx context.Context, rec *Record) error {
	if rec.SeqnoAfter != nil || rec.MsgHash == "" {
		return nil
	}
	status, err := c.broadcaster.Lookup(ctx, rec.MsgHash)
	if err != nil {
		status = LookupUnavailable
	}
	var next *uint32
	if status == LookupFound {
		n := uint32(1)
		if rec.SeqnoBefore != nil {
			n = *rec.SeqnoBefore + 1
		}
		next = &n
	} else if rec.SenderRaw != "" && rec.SeqnoBefore != nil {
		seq, serr := c.broadcaster.Seqno(ctx, rec.SenderRaw)
		if serr != nil {
			return nil
		}
		if seq > *rec.SeqnoBefore {
			s := seq
			next = &s
		}
	}
	if next == nil {
		return nil
	}
	rel, err := c.lockKey(ctx, rec.Key)
	if err != nil {
		return err
	}
	defer rel()
	cur, err := c.journal.Load(ctx, rec.Key)
	if err != nil {
		return err
	}
	if cur.SeqnoAfter != nil {
		rec.SeqnoAfter = cur.SeqnoAfter
		return nil
	}
	cur.SeqnoAfter = next
	cur.UpdatedAt = c.now()
	cur, err = c.save(ctx, cur)
	if err != nil {
		return err
	}
	*rec = cur
	return nil
}

func (c *Client) loadKey(ctx context.Context, key IdempotencyKey) (Record, error) {
	rel, err := c.lockKey(ctx, key)
	if err != nil {
		return Record{}, err
	}
	defer rel()
	return c.journal.Load(ctx, key)
}

func seqOrZero(v *uint32) uint32 {
	if v == nil {
		return 0
	}
	return *v
}

// keep time imported for readers of the poll budget.
var _ = time.Second
