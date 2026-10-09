// Package harnesses supplies the checked-in defaults copied into sandbox images.
package harnesses

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
)

//go:embed opencode.json
var openCodeConfig []byte

// OpenCode returns a fresh config so per-run model/agent fields never alter defaults.
func OpenCode() (map[string]any, error) {
	var config map[string]any
	err := json.Unmarshal(openCodeConfig, &config)
	return config, err
}

// OpenCodeDigest pins the same file used by direct CLI launches in the image.
func OpenCodeDigest() string {
	sum := sha256.Sum256(openCodeConfig)
	return hex.EncodeToString(sum[:])
}
