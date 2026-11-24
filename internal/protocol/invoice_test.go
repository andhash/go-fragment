package protocol

import (
	"encoding/json"
	"testing"
)

func TestParseTONAmountScale9(t *testing.T) {
	n, err := ParseTONAmount(json.RawMessage(`"0.5"`))
	if err != nil || n != 500_000_000 {
		t.Fatalf("0.5 -> %d %v", n, err)
	}
	if _, err := ParseTONAmount(json.RawMessage(`"1e9"`)); err == nil {
		t.Fatal("exponent accepted")
	}
	if _, err := ParseTONAmount(json.RawMessage(`"0.1234567891"`)); err == nil {
		t.Fatal("10 fractional digits accepted")
	}
	if _, err := ParseTONAmount(json.RawMessage(`"1,5"`)); err == nil {
		t.Fatal("comma accepted")
	}
}

func TestParseNanoTONScale0(t *testing.T) {
	n, err := ParseNanoTON(json.RawMessage(`"500000000"`))
	if err != nil || n != 500_000_000 {
		t.Fatalf("nano %d %v", n, err)
	}
	if _, err := ParseNanoTON(json.RawMessage(`"500000000.0"`)); err == nil {
		t.Fatal("fractional nanotons accepted")
	}
}

func TestParseLinkRejectsEVMAndZeroUntil(t *testing.T) {
	if _, err := ParseLink([]byte(`{"evm":{},"transaction":{"validUntil":1893456000,"from":"EQAGpMCtSbubdXHzw5wBaAt4Mm9tlvthnHUzqIMcmfdyIQ8s","messages":[{"address":"EQAGpMCtSbubdXHzw5wBaAt4Mm9tlvthnHUzqIMcmfdyIQ8s","amount":"1"}]}}`)); err != ErrUnexpected {
		t.Fatalf("evm: %v", err)
	}
	if _, err := ParseLink([]byte(`{"transaction":{"validUntil":null,"from":"EQAGpMCtSbubdXHzw5wBaAt4Mm9tlvthnHUzqIMcmfdyIQ8s","messages":[{"address":"EQAGpMCtSbubdXHzw5wBaAt4Mm9tlvthnHUzqIMcmfdyIQ8s","amount":"1"}]}}`)); err == nil {
		t.Fatal("null validUntil accepted")
	}
}

func TestDeviceFingerprintBytes(t *testing.T) {
	const want = `{"platform":"android","appName":"Tonkeeper","appVersion":"26.07.1","maxProtocolVersion":2,"features":["SendTransaction",{"name":"SignData","types":["text","binary","cell"]},{"name":"SendTransaction","maxMessages":255}]}`
	if DeviceFingerprint != want {
		t.Fatalf("device bytes differ")
	}
}

func TestHashFromHTML(t *testing.T) {
	got, ok := HashFromHTML([]byte(`<script src="/api?hash=0123456789abcdef"></script>`))
	if !ok || got != "0123456789abcdef" {
		t.Fatalf("hash %q %v", got, ok)
	}
}
