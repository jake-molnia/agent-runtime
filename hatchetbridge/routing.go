package hatchetbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/jake-molnia/agent-runtime/githubreview"
)

// ConfiguredWorkflowName isolates scheduler actions for each immutable snapshot.
// Truncating only the readable prefix preserves the full identity in the hash.
func ConfiguredWorkflowName(name, digest string) string {
	sum := sha256.Sum256([]byte(name + "\x00" + digest))
	if len(name) > 55 {
		name = name[:55]
	}
	return name + "-" + hex.EncodeToString(sum[:])
}
func ReviewRevision(config githubreview.Integration, digest string) (string, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append(raw, []byte(digest)...))
	return hex.EncodeToString(sum[:]), nil
}
