// Package fragment is an unofficial local client for fragment.com.
//
// Fragment does not publish a partner API for buying Stars or gifting
// Premium. This module talks to the site from the caller's process, signs
// with the caller's wallet, and returns control to the caller. It does not
// know any storefront, webhook, or order system. The caller passes a
// username, an amount, and its own idempotency key. A key prefix is opaque:
// the module does not reject or interpret one.
//
// The mnemonic never leaves the local signer. Session cookies go only to
// fragment.com. A chain endpoint receives neither. There is no telemetry.
//
// WithShowSender defaults to true when the option is omitted, matching the
// Fragment site. false hides the payer. The value is part of the idempotency
// identity. Client.Balance reads the hot-wallet balance. Pay still checks
// that balance before Sign.
//
// Pay returns when the broadcaster accepts the message (StateBroadcast) or
// when the stored row is already confirmed or settled. A nil error is not
// Stars delivery. Pay does not call confirmReq. Reconcile is the only caller
// of confirmReq. Two copies of the journal directory can pay the same invoice
// twice. Donation is off unless the caller passes WithDonation. The sweep
// account from the fragment-api-py 14.4.2 incident is never accepted.
//
// The default net/http fingerprint may be rejected by fragment.com. Pass
// WithHTTPDoer to supply another client. This module does not claim that the
// stock handshake is accepted.
package fragment
