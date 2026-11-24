// Command stars buys Telegram Stars for one username.
//
// It reads FRAGMENT_COOKIES_JSON, FRAGMENT_MNEMONIC, FRAGMENT_ORDER_ID,
// FRAGMENT_USERNAME, FRAGMENT_STARS and FRAGMENT_TREASURY. Empty values
// stop the process. The mnemonic is not printed. Pay spends TON from the
// hot wallet. A nil error means the transfer was broadcast, not that
// Fragment delivered the Stars. Delivery is Reconcile after confirmReq.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/andhash/go-fragment"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run() error {
	raw := os.Getenv("FRAGMENT_COOKIES_JSON")
	mnemonic := os.Getenv("FRAGMENT_MNEMONIC")
	orderID := os.Getenv("FRAGMENT_ORDER_ID")
	username := os.Getenv("FRAGMENT_USERNAME")
	starsText := os.Getenv("FRAGMENT_STARS")
	treasury := os.Getenv("FRAGMENT_TREASURY")
	if raw == "" || mnemonic == "" || orderID == "" || username == "" || starsText == "" || treasury == "" {
		return errors.New("FRAGMENT_COOKIES_JSON, FRAGMENT_MNEMONIC, FRAGMENT_ORDER_ID, FRAGMENT_USERNAME, FRAGMENT_STARS and FRAGMENT_TREASURY are required")
	}
	stars, err := strconv.Atoi(starsText)
	if err != nil {
		return errors.New("FRAGMENT_STARS is not an integer")
	}
	var cookies fragment.Cookies
	if err := json.Unmarshal([]byte(raw), &cookies); err != nil {
		return errors.New("FRAGMENT_COOKIES_JSON is not valid")
	}
	fmt.Fprintln(os.Stderr, "Pay spends TON from the hot wallet. nil means broadcast, not Stars delivery.")
	client, err := fragment.New(
		fragment.WithCookies(cookies),
		fragment.WithMnemonic(mnemonic),
		fragment.WithSpendLimit(mustLimit()),
		fragment.WithFragmentTreasury(treasury),
	)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pay, err := client.PayStars(ctx, username, stars, fragment.IdempotencyKey(orderID))
	if err != nil {
		return err
	}
	fmt.Printf("state=%s req_id=%s hash=%s\n", pay.State, pay.ReqID, pay.MsgHash)
	return nil
}

func mustLimit() int64 {
	text := os.Getenv("FRAGMENT_SPEND_LIMIT")
	if text == "" {
		return 2_000_000_000
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n <= 0 {
		return 2_000_000_000
	}
	return n
}
