package runner

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

func newIdempotencyKey() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate callback idempotency key: %w", err)
	}

	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80

	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(value[0:4]),
		hex.EncodeToString(value[4:6]),
		hex.EncodeToString(value[6:8]),
		hex.EncodeToString(value[8:10]),
		hex.EncodeToString(value[10:16]),
	), nil
}

func isUUIDv4(value string) bool {
	return isUUIDVersion(value, '4')
}

func isUUIDVersion(value string, version byte) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	if value[14] != version || !strings.ContainsRune("89abAB", rune(value[19])) {
		return false
	}

	_, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil
}
