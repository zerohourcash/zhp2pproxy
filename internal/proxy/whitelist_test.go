package proxy

import "testing"

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
	} {
		if MethodAllowed(method) {
			t.Fatalf("%s must not be allowed", method)
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
