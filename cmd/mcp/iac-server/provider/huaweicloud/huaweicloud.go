// Package huaweicloud implements provider.CloudProvider for HuaweiCloud.
//
// Skills (terraform deployment guide + examples, pricing guide, troubleshoot
// guide) are embedded at compile time via go:embed (see embed.go). iac-server
// extracts them to disk at startup for the standard skill loader and
// read/grep/ls tools.
//
// Catalog is queried via terraform data sources at plan time.
// Pricing is queried by the server LLM via http_request (BSS API, auto-signed)
// with WebSearch/WebFetch as a fallback.
package huaweicloud

import (
	"io/fs"
	"log/slog"
	"sync"

	openagent "github.com/yusheng-g/openagent-go"
	"github.com/yusheng-g/openagent-go/cmd/mcp/iac-server/provider"
	"github.com/yusheng-g/openagent-go/keyring"
)

// DefaultProviderMirror is the HuaweiCloud community provider mirror.
// Used as the default TF_PROVIDER_MIRRORS entry so terraform init works
// on networks where registry.terraform.io is slow or unreachable; user
// configured mirrors are appended after it.
const DefaultProviderMirror = "https://mirrors.huaweicloud.com/terraform/"

// HuaweiCloud implements provider.CloudProvider.
type HuaweiCloud struct {
	region string
	// k serves AK/SK/token reads from the Linux kernel user keyring
	// (KeyCtl), lazily opened on first use. A stub can be injected via
	// NewWithKeyring for tests. Never call Open() here — on machines with
	// a D-Bus Secret Service it would return the D-Bus backend, which is
	// a separate store from the kernel keyring the provisioning side
	// writes to.
	k kr
}

// kr is the lazily-initialized keyctl keyring accessor shared by Env()
// and HTTPRequest(). The sync.Once keeps the "credentials never persist
// in the struct" property: only the accessor is held, values are read
// fresh on every call.
type kr struct {
	once sync.Once
	k    keyring.Keyring
	err  error
}

func (c *kr) get(service, key string) string {
	c.once.Do(func() {
		// Pinned to the kernel keyring — see HuaweiCloud doc comment.
		c.k, c.err = keyring.OpenKeyctl()
		if c.err != nil {
			slog.Warn("kernel keyring unavailable — cloud credentials will be empty (provision via keyctl into the user keyring)", "error", c.err)
		}
	})
	if c.err != nil {
		return ""
	}
	v, err := c.k.Get(service, key)
	if err != nil {
		// Not-found is the normal "not provisioned" case — stay quiet.
		// Other failures (revoked key, permission) degrade to empty so
		// the tool layer surfaces its "credentials not configured" error.
		return ""
	}
	return v
}

// keyringService is the keyring service name. iac-server is built with
// version.Name=hwcloud (same LDFLAGS as the hwcloud CLI — see Makefile),
// so keys provisioned for the hwcloud binary are visible here too.
const keyringService = "hwcloud"

// New creates a HuaweiCloud provider for the given region.
// Credentials (AK/SK/token) are read from the Linux kernel user keyring
// on demand via Env(); the region comes from the environment.
func New(region string) *HuaweiCloud {
	return &HuaweiCloud{region: region}
}

// NewWithKeyring creates a HuaweiCloud provider with an injected keyring
// (for tests): the lazy OpenKeyctl() initialization is marked done and the
// stub installed, so get() reads through it directly.
func NewWithKeyring(region string, k keyring.Keyring) *HuaweiCloud {
	h := New(region)
	h.k.once.Do(func() { h.k.k = k })
	return h
}

// Compile-time interface check.
var _ provider.CloudProvider = (*HuaweiCloud)(nil)

// Name returns the cloud identifier.
func (h *HuaweiCloud) Name() string { return "huaweicloud" }

// Env returns HuaweiCloud credential environment variables for the
// terraform subprocess. AK/SK/token are read from the kernel keyring at
// call time (fresh values every call — a token refreshed in the keyring
// mid-process is picked up by the next call); the region comes from the
// constructor. Secrets never persist in the struct.
func (h *HuaweiCloud) Env() map[string]string {
	// All three credential keys are always present (token empty when not
	// provisioned): iac/client.go merges the iac-server process env as the
	// base and lets these values override it — an omitted key would let a
	// stale HW_SECURITY_TOKEN from the process env reach the terraform
	// subprocess. Empty is equivalent to unset for the terraform provider.
	return map[string]string{
		"HW_ACCESS_KEY":     h.k.get(keyringService, "HW_ACCESS_KEY"),
		"HW_SECRET_KEY":     h.k.get(keyringService, "HW_SECRET_KEY"),
		"HW_REGION":         h.region,
		"HW_SECURITY_TOKEN": h.k.get(keyringService, "HW_SECURITY_TOKEN"),
	}
}

// Skills returns the embedded skills directory.
func (h *HuaweiCloud) Skills() fs.FS { return Skills() }

// Agents returns the cloud-specific agent prompts and static skill names.
func (h *HuaweiCloud) Agents() map[provider.PromptRole]provider.AgentConfig {
	return h.agents()
}

// ProviderSource returns the terraform provider source for HuaweiCloud.
func (h *HuaweiCloud) ProviderSource() string { return "huaweicloud/huaweicloud" }

// HTTPRequest returns an http_request tool configured with HuaweiCloud
// credentials from the kernel keyring. The tool handles SDK-HMAC-SHA256
// signing automatically — the LLM never sees AK/SK.
func (h *HuaweiCloud) HTTPRequest() openagent.Tool {
	return NewHTTPRequest(
		h.k.get(keyringService, "HW_ACCESS_KEY"),
		h.k.get(keyringService, "HW_SECRET_KEY"),
		h.k.get(keyringService, "HW_SECURITY_TOKEN"), // temporary credentials; empty for permanent AK/SK
	)
}
