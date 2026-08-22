package proxy

import (
	"encoding/json"
	"testing"

	"github.com/zerohourcash/zhp2pproxy/pkg/jsonrpc"
)

func TestDangerousMethodsAreDenied(t *testing.T) {
	for _, method := range []string{
		"stop",
		"dumpprivkey",
		"importprivkey",
		"sendtoaddress",
		"sendmany",
		"pruneblockchain",
		"invalidateblock",
		"reconsiderblock",
		"signmessagewithprivkey",
	} {
		if MethodAllowed(method) {
			t.Fatalf("%s must not be allowed", method)
		}
	}
}

func TestOverlayRPCRejectsHeavyAndAdministrativeMethods(t *testing.T) {
	for _, method := range []string{
		"verifychain", "waitforlogs", "preciousblock", "savemempool", "signmessagewithprivkey",
	} {
		if P2PMethodAllowed(method) {
			t.Fatalf("%s must not be callable by an overlay peer", method)
		}
	}
	for _, method := range []string{"getblockcount", "getrawtransaction", "callcontract", "sendrawtransaction"} {
		if !P2PMethodAllowed(method) {
			t.Fatalf("%s should remain available to an overlay peer", method)
		}
	}
}

func TestOverlayRPCAllowsOnlyBoundedWalletScans(t *testing.T) {
	allowed := jsonrpc.Request{
		Method: "scantxoutset",
		Params: json.RawMessage(`["start",["addr(ZF8xrYQFkfW2YYuWb6UWhY57TXLt6sgobZ)","combo(021111111111111111111111111111111111111111111111111111111111111111)"]]`),
	}
	if !P2PRequestAllowed(allowed) {
		t.Fatal("bounded wallet scan should be allowed")
	}
	for _, params := range []string{
		`["status"]`,
		`["abort"]`,
		`["start",["raw(76a91400)"]]`,
		`["start",["combo(xprv-secret)"]]`,
		`["start",["addr(ZF8xrYQFkfW2YYuWb6UWhY57TXLt6sgobZ)","addr(ZF8xrYQFkfW2YYuWb6UWhY57TXLt6sgobZ)","addr(ZF8xrYQFkfW2YYuWb6UWhY57TXLt6sgobZ)"]]`,
	} {
		if P2PRequestAllowed(jsonrpc.Request{Method: "scantxoutset", Params: json.RawMessage(params)}) {
			t.Fatalf("unsafe scan params were accepted: %s", params)
		}
	}
}

func TestExpectedPublicMethodsAreAllowed(t *testing.T) {
	for _, method := range []string{
		"getblockcount",
		"getnetworkinfo",
		"getrawtransaction",
		"sendrawtransaction",
		"scantxoutset",
	} {
		if !MethodAllowed(method) {
			t.Fatalf("%s should be allowed", method)
		}
	}
}

func TestP2PConsensusMethodsExcludeMutableAndWriteRequests(t *testing.T) {
	for _, method := range []string{"getblock", "getblockheader", "getrawtransaction", "gettransactionreceipt"} {
		if !P2PConsensusRequired(method) {
			t.Fatalf("expected consensus for %s", method)
		}
	}
	for _, method := range []string{"getblockcount", "getrawmempool", "scantxoutset", "sendrawtransaction"} {
		if P2PConsensusRequired(method) {
			t.Fatalf("mutable or write method unexpectedly requires exact consensus: %s", method)
		}
	}
}
