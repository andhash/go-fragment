// Package protocol holds Fragment method names and the device fingerprint.
// It is the only package that knows those strings.
package protocol

// Flow is one product's page and method names from fragment-api-py commit 04a0601.
type Flow struct {
	Product       string
	Page          string
	SearchMethod  string
	StateMethod   string
	InitMethod    string
	LinkMethod    string
	ConfirmMethod string
}

// Stars buys Telegram Stars.
var Stars = Flow{
	Product:       "stars",
	Page:          "https://fragment.com/stars/buy",
	SearchMethod:  "searchStarsRecipient",
	StateMethod:   "updateStarsBuyState",
	InitMethod:    "initBuyStarsRequest",
	LinkMethod:    "getBuyStarsLink",
	ConfirmMethod: "confirmReq",
}

// Premium gifts Telegram Premium.
var Premium = Flow{
	Product:       "premium",
	Page:          "https://fragment.com/premium/gift",
	SearchMethod:  "searchPremiumGiftRecipient",
	StateMethod:   "updatePremiumState",
	InitMethod:    "initGiftPremiumRequest",
	LinkMethod:    "getGiftPremiumLink",
	ConfirmMethod: "confirmReq",
}
