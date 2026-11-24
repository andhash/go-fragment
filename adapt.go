package fragment

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/andhash/go-fragment/chain"
	"github.com/andhash/go-fragment/internal/transport"
	"github.com/andhash/go-fragment/journal"
	"github.com/andhash/go-fragment/wallet"
)

type transportAdapter struct{ c *transport.Client }

func (a transportAdapter) Call(ctx context.Context, call Call) (Reply, error) {
	reply, err := a.c.Call(ctx, transport.Call{Page: call.Page, Method: call.Method, Fields: call.Fields})
	if err != nil {
		if errors.Is(err, transport.ErrHashNotFound) {
			return Reply{}, ErrHashNotFound
		}
		return Reply{}, err
	}
	return Reply{HTTPStatus: reply.HTTPStatus, Body: reply.Body}, nil
}

type signerAdapter struct{ w *wallet.Local }

func (s signerAdapter) Account(context.Context) (Account, error) {
	raw, chainID, init, pub, err := s.w.Description()
	if err != nil {
		return Account{}, err
	}
	return Account{Address: raw, Chain: chainID, WalletStateInit: init, PublicKey: pub}, nil
}

func (s signerAdapter) Sign(ctx context.Context, tx PreparedTransaction, seqno uint32) (SignedMessage, error) {
	if tx.ValidUntil == nil || tx.ValidUntil.IsZero() {
		return SignedMessage{}, ErrInvoiceExpired
	}
	msgs := make([]wallet.Message, len(tx.Messages))
	for i, m := range tx.Messages {
		msgs[i] = wallet.Message{Address: m.Address, AmountNano: m.AmountNano, Payload: m.Payload, StateInit: m.StateInit}
	}
	boc, hash, err := s.w.Sign(ctx, *tx.ValidUntil, seqno, msgs)
	if err != nil {
		return SignedMessage{}, err
	}
	return SignedMessage{BOC: boc, MsgHash: hash, Seqno: seqno}, nil
}

type chainAdapter struct{ c *chain.Client }

func (a chainAdapter) Send(ctx context.Context, msg SignedMessage) error {
	return a.c.SendBOC(ctx, msg.BOC)
}

func (a chainAdapter) Lookup(ctx context.Context, msgHash string) (LookupStatus, error) {
	status, err := a.c.Lookup(ctx, msgHash)
	switch status {
	case "found":
		return LookupFound, err
	case "not_found":
		return LookupNotFound, err
	default:
		if err == nil {
			err = errors.New("chain lookup unavailable")
		}
		return LookupUnavailable, err
	}
}

func (a chainAdapter) BalanceNano(ctx context.Context, rawAddress string) (int64, error) {
	return a.c.Balance(ctx, rawAddress)
}

func (a chainAdapter) Seqno(ctx context.Context, rawAddress string) (uint32, error) {
	return a.c.Seqno(ctx, rawAddress)
}

type fileJournal struct{ f *journal.File }

func (j fileJournal) Create(ctx context.Context, rec Record) error {
	row, err := toRow(rec)
	if err != nil {
		return withCause(ErrJournal, err)
	}
	err = j.f.Create(ctx, row)
	return mapJournal(err)
}

func (j fileJournal) Load(ctx context.Context, key IdempotencyKey) (Record, error) {
	row, err := j.f.Load(ctx, string(key))
	if err != nil {
		return Record{}, mapJournal(err)
	}
	return fromRow(row)
}

func (j fileJournal) Store(ctx context.Context, rec Record) error {
	row, err := toRow(rec)
	if err != nil {
		return withCause(ErrJournal, err)
	}
	return mapJournal(j.f.Store(ctx, row))
}

func (j fileJournal) List(ctx context.Context) ([]Record, error) {
	rows, err := j.f.List(ctx)
	if err != nil {
		return nil, mapJournal(err)
	}
	out := make([]Record, 0, len(rows))
	for _, row := range rows {
		rec, err := fromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

func mapJournal(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, journal.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, journal.ErrExists):
		return errJournalExists
	case errors.Is(err, journal.ErrConflict):
		return ErrJournalConflict
	case errors.Is(err, journal.ErrSchema):
		return ErrJournalSchema
	case errors.Is(err, journal.ErrReqConflict):
		return ErrReqIDConflict
	default:
		return withCause(ErrJournal, err)
	}
}

var errJournalExists = coded("journal_exists", "payment already exists")

func toRow(rec Record) (journal.Row, error) {
	if rec.Messages == nil {
		rec.Messages = []OutgoingMessage{}
	}
	bd, err := json.Marshal(rec.Breakdown)
	if err != nil {
		return journal.Row{}, err
	}
	msgs, err := json.Marshal(rec.Messages)
	if err != nil {
		return journal.Row{}, err
	}
	return journal.Row{
		Schema:              1,
		Version:             rec.Version,
		Key:                 string(rec.Key),
		Product:             string(rec.Product),
		Username:            rec.Username,
		Quantity:            rec.Quantity,
		ShowSender:          rec.ShowSender,
		State:               string(rec.State),
		ReqID:               rec.ReqID,
		SenderRaw:           rec.SenderRaw,
		ValidUntil:          rec.ValidUntil,
		Breakdown:           bd,
		Messages:            msgs,
		MsgHash:             rec.MsgHash,
		BOC:                 rec.BOC,
		SeqnoBefore:         rec.SeqnoBefore,
		SeqnoAfter:          rec.SeqnoAfter,
		SendAttempted:       rec.SendAttempted,
		InitAttempts:        rec.InitAttempts,
		CreatedAt:           rec.CreatedAt,
		UpdatedAt:           rec.UpdatedAt,
		ResolutionEvidence:  rec.ResolutionEvidence,
		RecipientToken:      rec.RecipientToken,
		RecipientName:       rec.RecipientName,
		AccountJSON:         rec.AccountJSON,
		DonationAddress:     rec.DonationAddress,
		DonationBasisPoints: rec.DonationBasisPoints,
		DonationFixedNano:   rec.DonationFixedNano,
	}, nil
}

func fromRow(row journal.Row) (Record, error) {
	var bd AmountBreakdown
	if len(row.Breakdown) > 0 && string(row.Breakdown) != "null" {
		if err := json.Unmarshal(row.Breakdown, &bd); err != nil {
			return Record{}, withCause(ErrJournal, err)
		}
	}
	var msgs []OutgoingMessage
	if len(row.Messages) > 0 && string(row.Messages) != "null" {
		if err := json.Unmarshal(row.Messages, &msgs); err != nil {
			return Record{}, withCause(ErrJournal, err)
		}
	}
	if row.ValidUntil != nil && row.ValidUntil.IsZero() {
		row.ValidUntil = nil
	}
	return Record{
		Schema:              row.Schema,
		Version:             row.Version,
		Key:                 IdempotencyKey(row.Key),
		Product:             Product(row.Product),
		Username:            row.Username,
		Quantity:            row.Quantity,
		ShowSender:          row.ShowSender,
		State:               State(row.State),
		ReqID:               row.ReqID,
		SenderRaw:           row.SenderRaw,
		ValidUntil:          row.ValidUntil,
		Breakdown:           bd,
		Messages:            msgs,
		MsgHash:             row.MsgHash,
		BOC:                 row.BOC,
		SeqnoBefore:         row.SeqnoBefore,
		SeqnoAfter:          row.SeqnoAfter,
		SendAttempted:       row.SendAttempted,
		InitAttempts:        row.InitAttempts,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
		ResolutionEvidence:  row.ResolutionEvidence,
		RecipientToken:      row.RecipientToken,
		RecipientName:       row.RecipientName,
		AccountJSON:         row.AccountJSON,
		DonationAddress:     row.DonationAddress,
		DonationBasisPoints: row.DonationBasisPoints,
		DonationFixedNano:   row.DonationFixedNano,
	}, nil
}
