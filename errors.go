package fragment

import (
	"errors"
	"fmt"
)

// Error is a Fragment failure with a stable code. Compare it with errors.Is.
// The text is scrubbed and never includes a BOC, a cookie, or a mnemonic.
type Error struct {
	code  string
	msg   string
	cause error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	msg := e.msg
	if msg == "" {
		msg = e.code
	}
	if e.cause != nil {
		msg = msg + ": " + e.cause.Error()
	}
	return Scrub(msg)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *Error) Code() string {
	if e == nil {
		return ""
	}
	return e.code
}

func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok || e == nil || t == nil || t.code == "" {
		return false
	}
	return e.code == t.code
}

func coded(code, msg string) *Error {
	return &Error{code: code, msg: msg}
}

func withCause(sentinel *Error, cause error) *Error {
	return &Error{code: sentinel.code, msg: sentinel.msg, cause: cause}
}

var (
	ErrCookiesRequired        = coded("cookies_required", "fragment cookies are required")
	ErrIdempotencyKeyRequired = coded("idempotency_required", "idempotency key is required")
	ErrIdempotencyKey         = coded("idempotency_key", "idempotency key is invalid")
	ErrIdempotencyConflict    = coded("idempotency_conflict", "idempotency key is already used for a different order")
	ErrInvalidStarsAmount     = coded("invalid_stars_amount", "stars amount is outside 50..10000000")
	ErrInvalidMonths          = coded("invalid_months", "premium months must be 3, 6, or 12")
	ErrInvalidUsername        = coded("invalid_username", "username is invalid")
	ErrPhoneNotAccepted       = coded("phone_not_accepted", "phone numbers are not accepted")
	ErrUserNotFound           = coded("user_not_found", "fragment recipient was not found")
	ErrAlreadySubscribed      = coded("already_subscribed", "recipient already has Premium")
	ErrHashNotFound           = coded("hash_not_found", "fragment page has no api hash")
	ErrNoRequestID            = coded("no_request_id", "fragment init returned no request id")
	ErrPrepareInProgress      = coded("prepare_in_progress", "prepare is still in progress")
	ErrPrepareStuck           = coded("prepare_stuck", "prepare is stuck and needs a new idempotency key")
	ErrTreasuryUnpinned       = coded("treasury_unpinned", "fragment treasury is not pinned")
	ErrDestinationRejected    = coded("destination_rejected", "payment destination was rejected")
	ErrSpendLimit             = coded("spend_limit", "payment exceeds the spend limit")
	ErrInsufficientBalance    = coded("insufficient_balance", "hot wallet balance is below the quote")
	ErrBalanceUnavailable     = coded("balance_unavailable", "wallet balance is unavailable")
	ErrSeqnoUnavailable       = coded("seqno_unavailable", "wallet seqno is unavailable")
	ErrInvoiceExpired         = coded("invoice_expired", "invoice expired and needs a new idempotency key")
	ErrNotSent                = coded("not_sent", "signed payment was not sent")
	ErrChainUnavailable       = coded("chain_unavailable", "chain lookup is unavailable")
	ErrResumeRefused          = coded("resume_refused", "resume refused to send the stored payment")
	ErrOutcomeUnknown         = coded("outcome_unknown", "payment outcome is unknown")
	ErrWalletBusy             = coded("wallet_busy", "wallet has an unsettled seqno")
	ErrNotBroadcast           = coded("not_broadcast", "payment was resolved as not broadcast")
	ErrResolveRefused         = coded("resolve_refused", "resolve refused to change the payment")
	ErrBreakdownMismatch      = coded("breakdown_mismatch", "signed outputs do not match the prepared invoice")
	ErrReqIDConflict          = coded("req_id_conflict", "fragment request id belongs to another payment")
	ErrConfirmationTimeout    = coded("confirmation_timeout", "fragment confirmation timed out")
	ErrConfirmUnavailable     = coded("confirm_unavailable", "fragment confirm is unavailable")
	ErrJournal                = coded("journal", "journal write failed")
	ErrJournalConflict        = coded("journal_conflict", "journal version conflict")
	ErrJournalSchema          = coded("journal_schema", "journal schema is not supported")
	ErrNotFound               = coded("not_found", "payment was not found")
	ErrInvoiceMismatch        = coded("invoice_mismatch", "fragment invoice does not match the order")
	ErrUnexpectedPaymentFlow  = coded("unexpected_payment_flow", "fragment returned an unexpected payment flow")
	ErrTooManyMessages        = coded("too_many_messages", "fragment invoice has too many messages")
	ErrAccountRequired        = coded("account_required", "wallet account is required")
)

// OutcomeUnknownError means Send was already attempted. Do not refund and do not Pay again.
// It does not contain the BOC.
type OutcomeUnknownError struct {
	Key     IdempotencyKey
	MsgHash string
	Cause   error
}

func (e *OutcomeUnknownError) Error() string {
	return Scrub(fmt.Sprintf("payment outcome is unknown key=%s hash=%s", e.Key, e.MsgHash))
}

func (e *OutcomeUnknownError) Unwrap() error { return e.Cause }

func (e *OutcomeUnknownError) Code() string { return ErrOutcomeUnknown.code }

func (e *OutcomeUnknownError) Is(target error) bool {
	if _, ok := target.(*OutcomeUnknownError); ok {
		return true
	}
	return sameCode(target, ErrOutcomeUnknown.code)
}

// NotSentError means a BOC is durable and Send was not called. Pay does not send it.
// Resume is a separate call. The error does not contain the BOC.
type NotSentError struct {
	Key     IdempotencyKey
	MsgHash string
}

func (e *NotSentError) Error() string {
	return Scrub(fmt.Sprintf("signed payment was not sent key=%s hash=%s", e.Key, e.MsgHash))
}

func (e *NotSentError) Code() string { return ErrNotSent.code }

func (e *NotSentError) Is(target error) bool {
	if _, ok := target.(*NotSentError); ok {
		return true
	}
	return sameCode(target, ErrNotSent.code)
}

// InvoiceExpiredError means the stored req_id has a non-nil validUntil in the past and no BOC.
// A nil validUntil is not this error. The caller needs a new idempotency key.
type InvoiceExpiredError struct {
	Key IdempotencyKey
}

func (e *InvoiceExpiredError) Error() string {
	return Scrub(fmt.Sprintf("invoice expired key=%s", e.Key))
}

func (e *InvoiceExpiredError) Code() string { return ErrInvoiceExpired.code }

func (e *InvoiceExpiredError) Is(target error) bool {
	if _, ok := target.(*InvoiceExpiredError); ok {
		return true
	}
	return sameCode(target, ErrInvoiceExpired.code)
}

// ConfirmationTimeoutError is returned only by Reconcile after confirmReq succeeded
// and Fragment did not report done before ConfirmTimeout. It is not delivery.
// Error text does not contain the BOC.
type ConfirmationTimeoutError struct {
	Payment *Payment
}

func (e *ConfirmationTimeoutError) Error() string {
	key := IdempotencyKey("")
	if e != nil && e.Payment != nil {
		key = e.Payment.Key
	}
	return Scrub(fmt.Sprintf("fragment confirmation timed out key=%s", key))
}

func (e *ConfirmationTimeoutError) Code() string { return ErrConfirmationTimeout.code }

func (e *ConfirmationTimeoutError) Is(target error) bool {
	if _, ok := target.(*ConfirmationTimeoutError); ok {
		return true
	}
	return sameCode(target, ErrConfirmationTimeout.code)
}

// NotBroadcastError means Resolve already closed the payment as not broadcast.
// Error text does not contain the BOC.
type NotBroadcastError struct {
	Payment *Payment
}

func (e *NotBroadcastError) Error() string {
	key := IdempotencyKey("")
	if e != nil && e.Payment != nil {
		key = e.Payment.Key
	}
	return Scrub(fmt.Sprintf("payment was not broadcast key=%s", key))
}

func (e *NotBroadcastError) Code() string { return ErrNotBroadcast.code }

func (e *NotBroadcastError) Is(target error) bool {
	if _, ok := target.(*NotBroadcastError); ok {
		return true
	}
	return sameCode(target, ErrNotBroadcast.code)
}

func sameCode(target error, code string) bool {
	t, ok := target.(*Error)
	return ok && t.code == code
}

// APIError is an unclassified Fragment response. FragmentMessage is scrubbed and cut to 256 bytes.
type APIError struct {
	Method          string
	HTTPStatus      int
	FragmentMessage string
}

func (e *APIError) Error() string {
	msg := e.FragmentMessage
	if len(msg) > 256 {
		msg = msg[:256]
	}
	return Scrub(fmt.Sprintf("fragment %s: %s", e.Method, msg))
}

func (e *APIError) Code() string { return "fragment" }

// CodeOf returns the stable error code, or "error" when err has none.
func CodeOf(err error) string {
	if err == nil {
		return "ok"
	}
	var c interface{ Code() string }
	if errors.As(err, &c) {
		if code := c.Code(); code != "" {
			return code
		}
	}
	return "error"
}

// RetrySameKey reports whether Pay may be repeated with the same idempotency key.
// invoice_expired, prepare_stuck, and confirm_unavailable are not in this set.
func RetrySameKey(err error) bool {
	switch CodeOf(err) {
	case ErrWalletBusy.code, ErrPrepareInProgress.code, ErrInsufficientBalance.code,
		ErrBalanceUnavailable.code, ErrSeqnoUnavailable.code, ErrSpendLimit.code,
		ErrTreasuryUnpinned.code, ErrJournal.code:
		return true
	default:
		return false
	}
}
