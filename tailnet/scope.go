package tailnet

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// NewScope binds a deployment to the complete namespace inventory its workers can list.
// Deployment IDs must be stable across replicas and unique across deployments sharing a tailnet.
func NewScope(deploymentID string, namespaces []string) (string, error) {
	if deploymentID == "" || strings.TrimSpace(deploymentID) != deploymentID || len(namespaces) == 0 {
		return "", errors.New("tailnet ownership requires a deployment ID and namespaces")
	}
	namespaces = slices.Clone(namespaces)
	for _, namespace := range namespaces {
		if namespace == "" || strings.TrimSpace(namespace) != namespace {
			return "", errors.New("invalid tailnet ownership namespace")
		}
	}
	slices.Sort(namespaces)
	namespaces = slices.Compact(namespaces)
	data, err := json.Marshal(struct {
		Deployment string
		Namespaces []string
	}{deploymentID, namespaces})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:12]), nil
}

// Hostname maps a claim name to its deployment-scoped device hostname.
func (c *Client) Hostname(claim string) string {
	return "ar-" + c.Scope + "-" + strings.TrimPrefix(claim, "ar-")
}

func (c *Client) ownsHostname(hostname string) bool {
	prefix := "ar-" + c.Scope + "-"
	return strings.HasPrefix(hostname, prefix) && hostnamePattern.MatchString("ar-"+strings.TrimPrefix(hostname, prefix))
}
