// Package journal stores one schema-1 invoice per idempotency key.
// Two hosts with two copies of the directory will pay twice.
package journal

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const schema = 1

var (
	ErrNotFound    = errors.New("journal record not found")
	ErrExists      = errors.New("journal record exists")
	ErrConflict    = errors.New("journal version conflict")
	ErrSchema      = errors.New("journal schema")
	ErrReqConflict = errors.New("req id conflict")
)

// Row is the on-disk invoice. Callers in package fragment convert it to Record.
type Row struct {
	Schema              int             `json:"schema"`
	Version             int64           `json:"version"`
	Key                 string          `json:"key"`
	Product             string          `json:"product"`
	Username            string          `json:"username"`
	Quantity            int             `json:"quantity"`
	ShowSender          bool            `json:"show_sender"`
	State               string          `json:"state"`
	ReqID               string          `json:"req_id,omitempty"`
	SenderRaw           string          `json:"sender_raw,omitempty"`
	ValidUntil          *time.Time      `json:"valid_until"`
	Breakdown           json.RawMessage `json:"breakdown"`
	Messages            json.RawMessage `json:"messages"`
	MsgHash             string          `json:"msg_hash,omitempty"`
	BOC                 string          `json:"boc,omitempty"`
	SeqnoBefore         *uint32         `json:"seqno_before"`
	SeqnoAfter          *uint32         `json:"seqno_after"`
	SendAttempted       bool            `json:"send_attempted"`
	InitAttempts        int             `json:"init_attempts"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
	ResolutionEvidence  string          `json:"resolution_evidence,omitempty"`
	RecipientToken      string          `json:"recipient_token,omitempty"`
	RecipientName       string          `json:"recipient_name,omitempty"`
	AccountJSON         string          `json:"account_json,omitempty"`
	DonationAddress     string          `json:"donation_address,omitempty"`
	DonationBasisPoints int             `json:"donation_basis_points,omitempty"`
	DonationFixedNano   int64           `json:"donation_fixed_nano,omitempty"`
}

// File is a directory of JSON invoices. Lock files are not deleted.
type File struct {
	root string
}

// Open creates root/journal with mode 0700. HOME is the caller's concern.
func Open(root string) (*File, error) {
	dir := filepath.Join(root, "journal")
	if err := mkdir0700(dir); err != nil {
		return nil, err
	}
	if err := mkdir0700(filepath.Join(dir, "req")); err != nil {
		return nil, err
	}
	return &File{root: root}, nil
}

func (f *File) Create(_ context.Context, row Row) error {
	if row.Schema == 0 {
		row.Schema = schema
	}
	if row.Schema != schema {
		return ErrSchema
	}
	row.Version = 1
	data, err := marshal(row)
	if err != nil {
		return err
	}
	dir := filepath.Join(f.root, "journal")
	return createExclusive(dir, fileName(row.Key), data)
}

func (f *File) Load(_ context.Context, key string) (Row, error) {
	return f.load(key)
}

func (f *File) Store(_ context.Context, row Row) error {
	cur, err := f.load(row.Key)
	if err != nil {
		return err
	}
	if cur.Version != row.Version {
		return ErrConflict
	}
	if row.ReqID != "" {
		if err := f.claim(row.Key, row.ReqID); err != nil {
			return err
		}
	}
	row.Version = cur.Version + 1
	row.Schema = schema
	data, err := marshal(row)
	if err != nil {
		return err
	}
	dir := filepath.Join(f.root, "journal")
	if err := replaceFile(dir, fileName(row.Key), data); err != nil {
		return err
	}
	return nil
}

func (f *File) List(_ context.Context) ([]Row, error) {
	dir := filepath.Join(f.root, "journal")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Row
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.Contains(name, ".tmp-") {
			continue
		}
		row, err := readFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

func (f *File) load(key string) (Row, error) {
	return readFile(filepath.Join(f.root, "journal", fileName(key)))
}

func (f *File) claim(key, reqID string) error {
	dir := filepath.Join(f.root, "journal", "req")
	if err := mkdir0700(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, sha(reqID))
	fhandle, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if !os.IsExist(err) {
			return err
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if string(body) != key {
			return ErrReqConflict
		}
		return nil
	}
	defer fhandle.Close()
	if err := fhandle.Chmod(0600); err != nil {
		return err
	}
	if _, err := fhandle.Write([]byte(key)); err != nil {
		return err
	}
	return fhandle.Sync()
}

func readFile(path string) (Row, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Row{}, ErrNotFound
		}
		return Row{}, err
	}
	var probe struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return Row{}, err
	}
	if probe.Schema != schema {
		return Row{}, ErrSchema
	}
	var row Row
	if err := json.Unmarshal(body, &row); err != nil {
		return Row{}, err
	}
	if row.ValidUntil != nil && row.ValidUntil.IsZero() {
		row.ValidUntil = nil
	}
	return row, nil
}

func marshal(row Row) ([]byte, error) {
	if row.ValidUntil != nil && row.ValidUntil.IsZero() {
		row.ValidUntil = nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(row); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func fileName(key string) string { return sha(key) + ".json" }

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func mkdir0700(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return os.Chmod(path, 0700)
}

func createExclusive(dir, name string, data []byte) error {
	tmp, err := writeTemp(dir, name, data)
	if err != nil {
		return err
	}
	final := filepath.Join(dir, name)
	err = os.Link(tmp, final)
	_ = os.Remove(tmp)
	if err != nil {
		if os.IsExist(err) {
			return ErrExists
		}
		return err
	}
	return syncDir(dir)
}

func replaceFile(dir, name string, data []byte) error {
	tmp, err := writeTemp(dir, name, data)
	if err != nil {
		return err
	}
	final := filepath.Join(dir, name)
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(dir)
}

func writeTemp(dir, name string, data []byte) (string, error) {
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	tmp := filepath.Join(dir, name+".tmp-"+strconv.Itoa(os.Getpid())+"-"+hex.EncodeToString(rnd[:]))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return tmp, nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
