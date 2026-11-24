// Package wallet signs V4R2 and V5R1 transfers locally.
// It does not import net/http and it does not read a seqno from the network.
package wallet

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"
	"time"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

const (
	VersionV4R2 = "v4r2"
	VersionV5R1 = "v5r1"

	subwalletV4 uint32 = 698983191
	subwalletV5 uint32 = 0
	mainnetID   int32  = -239
	modePay     uint8  = 3
)

// Local is a hot wallet. The mnemonic stays in this process.
type Local struct {
	version string
	key     ed25519.PrivateKey
	pub     ed25519.PublicKey
	addr    *address.Address
	init    *tlb.StateInit
}

// Message is one internal output. Address is raw or friendly. Payload and StateInit are base64 BOC.
type Message struct {
	Address    string
	AmountNano int64
	Payload    string
	StateInit  string
}

// Output is one destination parsed back out of a signed external message.
type Output struct {
	Address    string
	AmountNano int64
}

// New checks the mnemonic locally and builds the contract address. It does not dial.
func New(mnemonic, version string) (*Local, error) {
	if version == "" {
		version = VersionV4R2
	}
	if version != VersionV4R2 && version != VersionV5R1 {
		return nil, errors.New("wallet version must be v4r2 or v5r1")
	}
	key, err := parseMnemonic(mnemonic)
	if err != nil {
		return nil, err
	}
	pub := key.Public().(ed25519.PublicKey)
	init, err := stateInit(version, pub)
	if err != nil {
		return nil, err
	}
	initCell, err := tlb.ToCell(init)
	if err != nil {
		return nil, err
	}
	addr := address.NewAddress(0, 0, initCell.Hash())
	return &Local{version: version, key: key, pub: pub, addr: addr, init: init}, nil
}

// Address is raw workchain:hex.
func (w *Local) Address() string { return w.addr.StringRaw() }

// Description returns the TON Connect account fields. Chain is mainnet "-239".
func (w *Local) Description() (raw, chain, stateInitB64, publicKeyHex string, err error) {
	c, err := tlb.ToCell(w.init)
	if err != nil {
		return "", "", "", "", err
	}
	return w.addr.StringRaw(), "-239", base64.StdEncoding.EncodeToString(c.ToBOC()), hex.EncodeToString(w.pub), nil
}

// Sign builds an external message for seqno and validUntil. It does not use the clock and it does not dial.
// The message hash is the hash of the external-message cell.
func (w *Local) Sign(_ context.Context, validUntil time.Time, seqno uint32, messages []Message) (boc, msgHash string, err error) {
	if validUntil.IsZero() {
		return "", "", errors.New("valid until is required")
	}
	limit := 4
	if w.version == VersionV5R1 {
		limit = 255
	}
	if len(messages) == 0 || len(messages) > limit {
		return "", "", errors.New("message count is not signable")
	}
	built := make([]*tlb.InternalMessage, len(messages))
	for i, m := range messages {
		im, err := internal(m)
		if err != nil {
			return "", "", err
		}
		built[i] = im
	}
	body, err := w.body(validUntil, seqno, built)
	if err != nil {
		return "", "", err
	}
	ext := &tlb.ExternalMessage{DstAddr: w.addr, Body: body}
	if seqno == 0 {
		ext.StateInit = w.init
	}
	root, err := tlb.ToCell(ext)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(root.ToBOC()), hex.EncodeToString(root.Hash()), nil
}

// ParseOutputs reads destination and amount from a signed external message.
func ParseOutputs(boc string) ([]Output, error) {
	raw, err := decodeB64(boc)
	if err != nil {
		return nil, err
	}
	root, err := cell.FromBOC(raw)
	if err != nil {
		return nil, err
	}
	var ext tlb.ExternalMessage
	if err := tlb.LoadFromCell(&ext, root.BeginParse()); err != nil {
		return nil, err
	}
	if ext.Body == nil {
		return nil, errors.New("external message has no body")
	}
	return outputsFromBody(ext.Body)
}

func (w *Local) body(validUntil time.Time, seqno uint32, messages []*tlb.InternalMessage) (*cell.Cell, error) {
	if w.version == VersionV5R1 {
		return w.bodyV5(validUntil, seqno, messages)
	}
	return w.bodyV4(validUntil, seqno, messages)
}

func (w *Local) bodyV4(validUntil time.Time, seqno uint32, messages []*tlb.InternalMessage) (*cell.Cell, error) {
	payload := cell.BeginCell().
		MustStoreUInt(uint64(subwalletV4), 32).
		MustStoreUInt(uint64(validUntil.UTC().Unix()), 32).
		MustStoreUInt(uint64(seqno), 32).
		MustStoreInt(0, 8)
	for _, m := range messages {
		c, err := tlb.ToCell(m)
		if err != nil {
			return nil, err
		}
		payload.MustStoreUInt(uint64(modePay), 8).MustStoreRef(c)
	}
	signed := payload.EndCell()
	sig := signed.Sign(w.key)
	return cell.BeginCell().MustStoreSlice(sig, 512).MustStoreBuilder(payload).EndCell(), nil
}

func (w *Local) bodyV5(validUntil time.Time, seqno uint32, messages []*tlb.InternalMessage) (*cell.Cell, error) {
	actions, err := packV5(messages)
	if err != nil {
		return nil, err
	}
	payload := cell.BeginCell().
		MustStoreUInt(0x7369676e, 32).
		MustStoreUInt(uint64(v5SerializedID()), 32).
		MustStoreUInt(uint64(validUntil.UTC().Unix()), 32).
		MustStoreUInt(uint64(seqno), 32).
		MustStoreBuilder(actions)
	signed := payload.EndCell()
	sig := signed.Sign(w.key)
	return cell.BeginCell().MustStoreBuilder(payload).MustStoreSlice(sig, 512).EndCell(), nil
}

func packV5(messages []*tlb.InternalMessage) (*cell.Builder, error) {
	list := cell.BeginCell().EndCell()
	for _, m := range messages {
		out, err := tlb.ToCell(m)
		if err != nil {
			return nil, err
		}
		action := cell.BeginCell().MustStoreUInt(0x0ec3c86d, 32).MustStoreUInt(uint64(modePay), 8).MustStoreRef(out)
		list = cell.BeginCell().MustStoreRef(list).MustStoreBuilder(action).EndCell()
	}
	return cell.BeginCell().MustStoreUInt(1, 1).MustStoreRef(list).MustStoreUInt(0, 1), nil
}

func v5SerializedID() uint32 {
	var ctx uint32
	ctx |= 1 << 31
	// Workchain 0 and wallet version 0 occupy bits that stay clear.
	ctx |= uint32(uint16(subwalletV5))
	// mainnetID is a typed constant. A direct uint32 conversion of -239
	// is rejected, so the conversion goes through a variable.
	network := mainnetID
	return ctx ^ uint32(network)
}

func stateInit(version string, pub ed25519.PublicKey) (*tlb.StateInit, error) {
	var data *cell.Cell
	var code *cell.Cell
	switch version {
	case VersionV4R2:
		code = mustCode(v4R2CodeHex)
		data = cell.BeginCell().
			MustStoreUInt(0, 32).
			MustStoreUInt(uint64(subwalletV4), 32).
			MustStoreSlice(pub, 256).
			MustStoreDict(nil).
			EndCell()
	case VersionV5R1:
		code = mustCode(v5R1CodeHex)
		data = cell.BeginCell().
			MustStoreBoolBit(true).
			MustStoreUInt(0, 32).
			MustStoreUInt(uint64(v5SerializedID()), 32).
			MustStoreSlice(pub, 256).
			MustStoreDict(nil).
			EndCell()
	default:
		return nil, errors.New("wallet version")
	}
	return &tlb.StateInit{Code: code, Data: data}, nil
}

func mustCode(hexBOC string) *cell.Cell {
	raw, err := hex.DecodeString(hexBOC)
	if err != nil {
		panic(err)
	}
	c, err := cell.FromBOC(raw)
	if err != nil {
		panic(err)
	}
	return c
}

func internal(m Message) (*tlb.InternalMessage, error) {
	if m.AmountNano <= 0 {
		return nil, errors.New("amount")
	}
	addr, err := parseAddr(m.Address)
	if err != nil {
		return nil, err
	}
	msg := &tlb.InternalMessage{
		IHRDisabled: true,
		Bounce:      addr.IsBounceable(),
		DstAddr:     addr,
		Amount:      tlb.FromNanoTON(big.NewInt(m.AmountNano)),
	}
	if m.Payload != "" {
		raw, err := decodeB64(m.Payload)
		if err != nil {
			return nil, err
		}
		c, err := cell.FromBOC(raw)
		if err != nil {
			return nil, err
		}
		msg.Body = c
	}
	if m.StateInit != "" {
		raw, err := decodeB64(m.StateInit)
		if err != nil {
			return nil, err
		}
		c, err := cell.FromBOC(raw)
		if err != nil {
			return nil, err
		}
		var st tlb.StateInit
		if err := tlb.LoadFromCell(&st, c.BeginParse()); err != nil {
			return nil, err
		}
		msg.StateInit = &st
	}
	return msg, nil
}

func parseAddr(s string) (*address.Address, error) {
	if a, err := address.ParseRawAddr(s); err == nil {
		return a, nil
	}
	return address.ParseAddr(s)
}

func outputsFromBody(body *cell.Cell) ([]Output, error) {
	s := body.BeginParse()
	op, err := s.LoadUInt(32)
	if err != nil {
		return nil, err
	}
	if op == 0x7369676e {
		return outputsV5(s)
	}
	s = body.BeginParse()
	if _, err := s.LoadSlice(512); err != nil {
		return nil, err
	}
	if _, err := s.LoadUInt(32); err != nil {
		return nil, err
	}
	if _, err := s.LoadUInt(32); err != nil {
		return nil, err
	}
	if _, err := s.LoadUInt(32); err != nil {
		return nil, err
	}
	if _, err := s.LoadUInt(8); err != nil {
		return nil, err
	}
	var out []Output
	for s.RefsNum() > 0 {
		if _, err := s.LoadUInt(8); err != nil {
			return nil, err
		}
		ref, err := s.LoadRefCell()
		if err != nil {
			return nil, err
		}
		one, err := outputFromInternal(ref)
		if err != nil {
			return nil, err
		}
		out = append(out, one)
	}
	return out, nil
}

func outputsV5(s *cell.Slice) ([]Output, error) {
	if _, err := s.LoadUInt(32); err != nil {
		return nil, err
	}
	if _, err := s.LoadUInt(32); err != nil {
		return nil, err
	}
	if _, err := s.LoadUInt(32); err != nil {
		return nil, err
	}
	flag, err := s.LoadUInt(1)
	if err != nil || flag != 1 {
		return nil, errors.New("v5 actions")
	}
	list, err := s.LoadRefCell()
	if err != nil {
		return nil, err
	}
	var rev []Output
	for list.BitsSize() > 0 || list.RefsNum() > 0 {
		sl := list.BeginParse()
		prev, err := sl.LoadRefCell()
		if err != nil {
			return nil, err
		}
		op, err := sl.LoadUInt(32)
		if err != nil || op != 0x0ec3c86d {
			return nil, errors.New("v5 action")
		}
		if _, err := sl.LoadUInt(8); err != nil {
			return nil, err
		}
		ref, err := sl.LoadRefCell()
		if err != nil {
			return nil, err
		}
		one, err := outputFromInternal(ref)
		if err != nil {
			return nil, err
		}
		rev = append(rev, one)
		list = prev
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, nil
}

func outputFromInternal(c *cell.Cell) (Output, error) {
	var msg tlb.InternalMessage
	if err := tlb.LoadFromCell(&msg, c.BeginParse()); err != nil {
		return Output{}, err
	}
	if msg.DstAddr == nil || msg.Amount.Nano() == nil || !msg.Amount.Nano().IsInt64() {
		return Output{}, errors.New("internal message")
	}
	return Output{Address: msg.DstAddr.StringRaw(), AmountNano: msg.Amount.Nano().Int64()}, nil
}

func decodeB64(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}
