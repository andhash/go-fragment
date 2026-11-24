package fragment

import (
	"context"
	"fmt"
	"time"
)

// Record is journal schema 1: one invoice per idempotency key.
// fmt prints the hash and the state, not the BOC.
type Record struct {
	Schema              int               `json:"schema"`
	Version             int64             `json:"version"`
	Key                 IdempotencyKey    `json:"key"`
	Product             Product           `json:"product"`
	Username            string            `json:"username"`
	Quantity            int               `json:"quantity"`
	ShowSender          bool              `json:"show_sender"`
	State               State             `json:"state"`
	ReqID               string            `json:"req_id,omitempty"`
	SenderRaw           string            `json:"sender_raw,omitempty"`
	ValidUntil          *time.Time        `json:"valid_until"`
	Breakdown           AmountBreakdown   `json:"breakdown"`
	Messages            []OutgoingMessage `json:"messages"`
	MsgHash             string            `json:"msg_hash,omitempty"`
	BOC                 string            `json:"boc,omitempty"`
	SeqnoBefore         *uint32           `json:"seqno_before"`
	SeqnoAfter          *uint32           `json:"seqno_after"`
	SendAttempted       bool              `json:"send_attempted"`
	InitAttempts        int               `json:"init_attempts"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
	ResolutionEvidence  string            `json:"resolution_evidence,omitempty"`
	RecipientToken      string            `json:"recipient_token,omitempty"`
	RecipientName       string            `json:"recipient_name,omitempty"`
	AccountJSON         string            `json:"account_json,omitempty"`
	DonationAddress     string            `json:"donation_address,omitempty"`
	DonationBasisPoints int               `json:"donation_basis_points,omitempty"`
	DonationFixedNano   int64             `json:"donation_fixed_nano,omitempty"`
}

// SignedBOC returns the stored external-message BOC for Reconcile and the operator.
// Do not write it into a ticket or a chat.
func (r Record) SignedBOC() string { return r.BOC }

func (r Record) String() string {
	return fmt.Sprintf("payment key=%s state=%s hash=%s", r.Key, r.State, r.MsgHash)
}

func (r Record) GoString() string { return r.String() }

// PaymentJournal stores one schema-1 invoice per key.
// Store is a compare-and-swap on Version and is valid only while the caller
// holds that key's lock across load, version check, temp file, rename, and directory sync.
type PaymentJournal interface {
	Create(ctx context.Context, rec Record) error
	Load(ctx context.Context, key IdempotencyKey) (Record, error)
	Store(ctx context.Context, rec Record) error
	List(ctx context.Context) ([]Record, error)
}

// Locker is not reentrant. Acquire of a name this process already holds blocks
// until that holder releases it. The only order is the idempotency key, then
// the raw address. Release the address first.
type Locker interface {
	Acquire(ctx context.Context, name string) (release func(), err error)
}
