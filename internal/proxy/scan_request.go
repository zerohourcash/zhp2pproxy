package proxy

import (
	"encoding/json"
	"regexp"

	"github.com/zerohourcash/zhp2pproxy/internal/multichain"
	"github.com/zerohourcash/zhp2pproxy/pkg/jsonrpc"
)

var (
	zhcAddressScanDescriptor  = regexp.MustCompile(`^addr\(Z[1-9A-HJ-NP-Za-km-z]{25,49}\)$`)
	compressedComboDescriptor = regexp.MustCompile(`^combo\((?:02|03)[0-9a-fA-F]{64}\)$`)
)

// P2PRequestAllowed applies parameter-level policy in addition to the method
// whitelist. A remote peer may run only a small wallet-owned UTXO scan: no
// ranges, xpub/xprv descriptors, raw scripts, status/abort, or large batches.
func P2PRequestAllowed(req jsonrpc.Request) bool {
	if !P2PMethodAllowed(req.Method) {
		return false
	}
	if req.Method == "multichain_rpc" {
		return multichain.RequestAllowed(req)
	}
	if req.Method != "scantxoutset" {
		return true
	}

	var params []json.RawMessage
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params) != 2 {
		return false
	}
	var action string
	if err := json.Unmarshal(params[0], &action); err != nil || action != "start" {
		return false
	}
	var descriptors []string
	if err := json.Unmarshal(params[1], &descriptors); err != nil || len(descriptors) < 1 || len(descriptors) > 2 {
		return false
	}
	for _, descriptor := range descriptors {
		if !zhcAddressScanDescriptor.MatchString(descriptor) &&
			!compressedComboDescriptor.MatchString(descriptor) {
			return false
		}
	}
	return true
}
