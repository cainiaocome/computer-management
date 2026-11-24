// Package protocol defines the wire format shared by the server and the agent:
// the signed policy bundle and the check-in/report payloads.
package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Schema is the current bundle schema version.
const Schema = 1

// SigningContext is mixed into the signature to stop a signature from one
// context being replayed in another.
const SigningContext = "fleet-policy-bundle-v1"

var (
	ErrBadSignature      = errors.New("policy bundle signature is not valid")
	ErrStaleGeneration   = errors.New("policy bundle generation is older than the applied one")
	ErrUnsupportedSchema = errors.New("unsupported policy bundle schema")
)

// Item is one declarative policy entry.
type Item struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Class   string          `json:"class"`
	Enabled bool            `json:"enabled"`
	Config  json.RawMessage `json:"config,omitempty"`
}

// Bundle is the signed unit of desired state delivered to a device.
//
// The signature covers the canonical JSON encoding of the bundle with
// Signature cleared, prefixed with SigningContext.
type Bundle struct {
	Schema     int       `json:"schema"`
	DeviceID   string    `json:"device_id"`
	Generation int64     `json:"generation"`
	IssuedAt   time.Time `json:"issued_at"`
	Policies   []Item    `json:"policies"`
	Signature  string    `json:"signature,omitempty"`
}

// signableBytes returns the deterministic byte string that is signed/verified.
//
// encoding/json is deterministic for these types (struct field order is fixed,
// maps are sorted, RawMessage is compacted), which is what makes this safe
// without pulling in a canonical-JSON library.
func signableBytes(b *Bundle) ([]byte, error) {
	unsigned := *b
	unsigned.Signature = ""
	body, err := json.Marshal(unsigned)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(SigningContext)+1+len(body))
	out = append(out, SigningContext...)
	out = append(out, '\n')
	out = append(out, body...)
	return out, nil
}

// Sign sets b.Signature using priv.
func Sign(b *Bundle, priv ed25519.PrivateKey) error {
	msg, err := signableBytes(b)
	if err != nil {
		return err
	}
	b.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg))
	return nil
}

// Verify checks b.Signature against pub.
func Verify(b *Bundle, pub ed25519.PublicKey) error {
	if b.Schema != Schema {
		return fmt.Errorf("%w: got %d, want %d", ErrUnsupportedSchema, b.Schema, Schema)
	}
	if b.Signature == "" {
		return ErrBadSignature
	}
	sig, err := base64.StdEncoding.DecodeString(b.Signature)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, mustSignable(b), sig) {
		return ErrBadSignature
	}
	return nil
}

func mustSignable(b *Bundle) []byte {
	msg, err := signableBytes(b)
	if err != nil {
		// Only happens if the bundle contains an unencodable value, which
		// cannot come out of json.Unmarshal.
		panic(err)
	}
	return msg
}

// EncodePublicKey renders a public key for the wire (base64, standard).
func EncodePublicKey(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// DecodePublicKey parses a public key from the wire.
func DecodePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// GenerateKey creates a fresh Ed25519 keypair.
func GenerateKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// --- enrollment / check-in payloads ---

type EnrollRequest struct {
	Token    string `json:"token"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
}

type EnrollResponse struct {
	DeviceID     string    `json:"device_id"`
	DeviceSecret string    `json:"device_secret"`
	SigningKey   string    `json:"signing_key"`
	ServerTime   time.Time `json:"server_time"`
}

type ModuleReport struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Class   string         `json:"class"`
	Status  string         `json:"status"`
	Message string         `json:"message,omitempty"`
	Detail  map[string]any `json:"detail,omitempty"`
}

type CheckinRequest struct {
	Hostname   string         `json:"hostname"`
	OS         string         `json:"os"`
	OSVersion  string         `json:"os_version,omitempty"`
	Arch       string         `json:"arch"`
	Version    string         `json:"version"`
	Generation int64          `json:"generation"`
	Reports    []ModuleReport `json:"reports,omitempty"`
}

type CheckinResponse struct {
	// Bundle is nil when the device has no policy assigned.
	Bundle *Bundle `json:"bundle,omitempty"`
}

// Report statuses.
const (
	StatusCompliant      = "compliant"
	StatusChanged        = "changed"
	StatusDrifted        = "drifted"
	StatusActionRequired = "action_required"
	StatusUnsupported    = "unsupported"
	StatusError          = "error"
	StatusSkipped        = "skipped"
)
