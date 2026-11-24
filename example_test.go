package fragment_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/andhash/go-fragment"
)

// ExampleClient_PrepareStars uses a fake transport. It does not spend TON.
func ExampleClient_PrepareStars() {
	dir, err := os.MkdirTemp("", "go-fragment-example")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	client, err := fragment.New(
		fragment.WithTransport(exampleTransport{}),
		fragment.WithAccount(fragment.Account{
			Address:         "0:141ba1c16d8db3786495c4ad4faf87533e8955301ee8c38c07a55b5ee515a318",
			Chain:           "-239",
			WalletStateInit: "te6cckEBAQEAAgAAAEysuc0=",
			PublicKey:       "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
		}),
		fragment.WithJournalDir(dir),
		fragment.WithClock(func() time.Time { return time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC) }),
	)
	if err != nil {
		panic(err)
	}
	pp, err := client.PrepareStars(context.Background(), "alice", 50, "order-1001")
	if err != nil {
		panic(err)
	}
	fmt.Println(pp.State, pp.Breakdown.RequiredNanoTON)
	// Output:
	// prepared 550000000
}

type exampleTransport struct{ step int }

func (t exampleTransport) Call(_ context.Context, call fragment.Call) (fragment.Reply, error) {
	var body string
	switch call.Method {
	case "updateStarsBuyState":
		body = `{"ok":true}`
	case "searchStarsRecipient":
		body = `{"found":{"recipient":"recipient-token","name":"Ada"}}`
	case "initBuyStarsRequest":
		body = `{"req_id":"req-stars-1","amount":"0.5"}`
	case "getBuyStarsLink":
		body = `{"transaction":{"validUntil":1893456000,"from":"EQAUG6HBbY2zeGSVxK1Pr4dTPolVMB7ow4wHpVte5RWjGEOM","messages":[{"address":"EQAGpMCtSbubdXHzw5wBaAt4Mm9tlvthnHUzqIMcmfdyIQ8s","amount":"500000000"}]}}`
	default:
		body = `{"error":"unexpected"}`
	}
	return fragment.Reply{HTTPStatus: 200, Body: json.RawMessage(body)}, nil
}
