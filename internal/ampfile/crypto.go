package ampfile

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
)

const (
	keyFragmentName  = "k"
	hashFragmentName = "h"
	encryptedMagic   = "AMPFENC1"
)

func randomBytes(n int) ([]byte, error) {
	out := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, out); err != nil {
		return nil, err
	}
	return out, nil
}

func deriveSubkey(master []byte, label string) []byte {
	mac := hmac.New(sha256.New, master)
	_, _ = mac.Write([]byte(label))
	return mac.Sum(nil)
}

func contentKey(master []byte) []byte {
	return deriveSubkey(master, "ampcache-file-content-v1")
}

func pathKey(master []byte) []byte {
	return deriveSubkey(master, "ampcache-file-path-v1")
}

func encryptPayload(key []byte, aad string, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce, err := randomBytes(gcm.NonceSize())
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(encryptedMagic)+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, encryptedMagic...)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, []byte(aad))
	return out, nil
}

func decryptPayload(key []byte, aad string, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < len(encryptedMagic) || string(ciphertext[:len(encryptedMagic)]) != encryptedMagic {
		return nil, errors.New("encrypted payload magic mismatch")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	rest := ciphertext[len(encryptedMagic):]
	if len(rest) < gcm.NonceSize() {
		return nil, errors.New("encrypted payload is too short")
	}
	nonce := rest[:gcm.NonceSize()]
	body := rest[gcm.NonceSize():]
	return gcm.Open(nil, nonce, body, []byte(aad))
}

func symbolLogicalID(runID string, chunkIndex, symbolIndex uint64) string {
	return fmt.Sprintf("%s/c/%08d/s/%08d", runID, chunkIndex, symbolIndex)
}

func pageLogicalID(runID string, pageIndex uint64) string {
	return fmt.Sprintf("%s/m/%08d", runID, pageIndex)
}

func aadRoot() string {
	return "ampfile:root"
}

func aadPage(runID string, pageIndex uint64) string {
	return "ampfile:page:" + pageLogicalID(runID, pageIndex)
}

func aadSymbol(runID string, chunkIndex, symbolIndex uint64) string {
	return "ampfile:symbol:" + symbolLogicalID(runID, chunkIndex, symbolIndex)
}

func pathToken(key []byte, logicalID string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(logicalID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func randomPathToken() (string, error) {
	b, err := randomBytes(32)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func keyedURL(cacheURL string, master, manifestHash []byte) string {
	fragment := keyFragmentName + "=" + base64.RawURLEncoding.EncodeToString(master)
	if len(manifestHash) > 0 {
		fragment += "&" + hashFragmentName + "=" + base64.RawURLEncoding.EncodeToString(manifestHash)
	}
	u, err := url.Parse(cacheURL)
	if err != nil {
		return cacheURL + "#" + fragment
	}
	u.Fragment = fragment
	return u.String()
}

func splitKeyedURL(value string) (cacheURL string, master []byte, manifestHash []byte, err error) {
	u, err := url.Parse(value)
	if err != nil {
		return "", nil, nil, err
	}
	fragment := u.Fragment
	u.Fragment = ""
	if fragment == "" {
		return "", nil, nil, errors.New("manifest URL must include #k=<key>")
	}
	var encoded string
	var encodedHash string
	if values, err := url.ParseQuery(fragment); err == nil && values.Get(keyFragmentName) != "" {
		encoded = values.Get(keyFragmentName)
		encodedHash = values.Get(hashFragmentName)
	} else {
		encoded = fragment
	}
	key, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, nil, fmt.Errorf("decode key fragment: %w", err)
	}
	if len(key) != 32 {
		return "", nil, nil, fmt.Errorf("key fragment is %d bytes, want 32", len(key))
	}
	if encodedHash != "" {
		manifestHash, err = base64.RawURLEncoding.DecodeString(encodedHash)
		if err != nil {
			return "", nil, nil, fmt.Errorf("decode manifest hash fragment: %w", err)
		}
		if len(manifestHash) != 32 {
			return "", nil, nil, fmt.Errorf("manifest hash fragment is %d bytes, want 32", len(manifestHash))
		}
	}
	return u.String(), key, manifestHash, nil
}
