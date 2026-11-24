package wallet_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/xssnick/tonutils-go/ton/wallet"

	local "github.com/andhash/go-fragment/wallet"
)

func TestAddressMatchesTonutils(t *testing.T) {
	phrase := mustPhrase(t)
	seed := strings.Fields(phrase)

	v4, err := local.New(phrase, local.VersionV4R2)
	if err != nil {
		t.Fatal(err)
	}
	ref4, err := wallet.FromSeed(nil, seed, wallet.V4R2)
	if err != nil {
		t.Fatal(err)
	}
	if v4.Address() != ref4.Address().StringRaw() {
		t.Fatalf("v4 address %s want %s", v4.Address(), ref4.Address().StringRaw())
	}

	v5, err := local.New(phrase, local.VersionV5R1)
	if err != nil {
		t.Fatal(err)
	}
	ref5, err := wallet.FromSeed(nil, seed, wallet.ConfigV5R1Final{NetworkGlobalID: wallet.MainnetGlobalID})
	if err != nil {
		t.Fatal(err)
	}
	if v5.Address() != ref5.Address().StringRaw() {
		t.Fatalf("v5 address %s want %s", v5.Address(), ref5.Address().StringRaw())
	}
}

func TestSignRoundTrip(t *testing.T) {
	phrase := mustPhrase(t)
	until := time.Unix(1893456000, 0)
	msg := local.Message{
		Address:    "0:06a4c0ad49bb9b7571f3c39c01680b78326f6d96fb619c7533a8831c99f77221",
		AmountNano: 500_000_000,
	}
	for _, ver := range []string{local.VersionV4R2, local.VersionV5R1} {
		w, err := local.New(phrase, ver)
		if err != nil {
			t.Fatal(err)
		}
		boc, hash, err := w.Sign(context.Background(), until, 1, []local.Message{msg})
		if err != nil {
			t.Fatalf("%s sign: %v", ver, err)
		}
		if hash == "" || boc == "" {
			t.Fatalf("%s empty signature", ver)
		}
		outs, err := local.ParseOutputs(boc)
		if err != nil {
			t.Fatalf("%s parse: %v", ver, err)
		}
		if len(outs) != 1 || outs[0].AmountNano != msg.AmountNano || outs[0].Address != msg.Address {
			t.Fatalf("%s outputs %+v", ver, outs)
		}
		if _, _, err := w.Sign(context.Background(), time.Time{}, 1, []local.Message{msg}); err == nil {
			t.Fatalf("%s signed a zero validUntil", ver)
		}
	}
}

func TestRejectShortMnemonic(t *testing.T) {
	if _, err := local.New("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about", local.VersionV4R2); err == nil {
		t.Fatal("12 words accepted")
	}
}

func TestWalletProductionHasNoNetHTTP(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatal(err, string(out))
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "net/http" {
			t.Fatal("wallet production import graph includes net/http")
		}
	}
}

func mustPhrase(t *testing.T) string {
	t.Helper()
	phrase, err := local.GenerateMnemonic()
	if err != nil {
		t.Fatal(err)
	}
	return phrase
}
