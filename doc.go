// Package fragment is an unofficial local client for fragment.com.
//
// Fragment does not publish a partner API for buying Stars or gifting
// Premium. This module talks to the site from the caller's process, signs
// with the caller's wallet, and returns control to the caller. It does not
// know any storefront, webhook, or order system. The caller passes a
// username, an amount, and its own idempotency key.
//
// The mnemonic never leaves the local signer. Session cookies go only to
// fragment.com. A chain endpoint receives neither.
//
// The purchase flow is specified in DESIGN.md and is not implemented yet.
// This package currently documents the module path and the boundary.
package fragment
