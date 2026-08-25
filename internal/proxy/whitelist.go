package proxy

var allowedMethods = map[string]struct{}{
	"getblockchaininfo":     {},
	"getblockcount":         {},
	"getblockhash":          {},
	"getblock":              {},
	"getblockheader":        {},
	"getbestblockhash":      {},
	"getdifficulty":         {},
	"getchaintips":          {},
	"getchaintxstats":       {},
	"getnetworkinfo":        {},
	"getrawmempool":         {},
	"getmempoolinfo":        {},
	"getmempoolentry":       {},
	"getmempoolancestors":   {},
	"getmempooldescendants": {},
	"gettxout":              {},
	"gettxoutsetinfo":       {},
	"verifychain":           {},
	"verifytxoutproof":      {},
	"gettxoutproof":         {},
	"preciousblock":         {},
	"savemempool":           {},
	"createrawtransaction":  {},
	"decoderawtransaction":  {},
	"decodescript":          {},
	"getrawtransaction":     {},
	"sendrawtransaction":    {},
	"testmempoolaccept":     {},
	"combinerawtransaction": {},
	"validateaddress":       {},
	"verifymessage":         {},
	"gethexaddress":         {},
	"fromhexaddress":        {},
	"callcontract":          {},
	"getcontractcode":       {},
	"getaccountinfo":        {},
	"getstorage":            {},
	"listcontracts":         {},
	"searchlogs":            {},
	"waitforlogs":           {},
	"gettransactionreceipt": {},
	"scantxoutset":          {},
	"multichain_rpc":        {},
}

var p2pFirstMethods = map[string]struct{}{
	"getblockchaininfo":     {},
	"getblockcount":         {},
	"getblockhash":          {},
	"getblock":              {},
	"getblockheader":        {},
	"getbestblockhash":      {},
	"getdifficulty":         {},
	"getchaintips":          {},
	"getchaintxstats":       {},
	"getnetworkinfo":        {},
	"getrawmempool":         {},
	"getmempoolinfo":        {},
	"getmempoolentry":       {},
	"getmempoolancestors":   {},
	"getmempooldescendants": {},
	"gettxout":              {},
	"verifytxoutproof":      {},
	"gettxoutproof":         {},
	"decoderawtransaction":  {},
	"decodescript":          {},
	"getrawtransaction":     {},
	"validateaddress":       {},
	"verifymessage":         {},
	"gethexaddress":         {},
	"fromhexaddress":        {},
	"callcontract":          {},
	"getcontractcode":       {},
	"getaccountinfo":        {},
	"getstorage":            {},
	"listcontracts":         {},
	"searchlogs":            {},
	"gettransactionreceipt": {},
}

// p2pAllowedMethods is intentionally narrower than the loopback/public HTTP
// API. Overlay peers may query ordinary chain state and relay a pre-signed
// transaction and a separately validated/rate-limited wallet UTXO scan, but
// cannot trigger arbitrary scans, local maintenance, signing, or
// chain-selection operations on somebody else's node.
var p2pAllowedMethods = map[string]struct{}{
	"getblockchaininfo":     {},
	"getblockcount":         {},
	"getblockhash":          {},
	"getblock":              {},
	"getblockheader":        {},
	"getbestblockhash":      {},
	"getdifficulty":         {},
	"getchaintips":          {},
	"getchaintxstats":       {},
	"getnetworkinfo":        {},
	"getrawmempool":         {},
	"getmempoolinfo":        {},
	"getmempoolentry":       {},
	"getmempoolancestors":   {},
	"getmempooldescendants": {},
	"gettxout":              {},
	"verifytxoutproof":      {},
	"gettxoutproof":         {},
	"decoderawtransaction":  {},
	"decodescript":          {},
	"getrawtransaction":     {},
	"sendrawtransaction":    {},
	"testmempoolaccept":     {},
	"validateaddress":       {},
	"verifymessage":         {},
	"gethexaddress":         {},
	"fromhexaddress":        {},
	"callcontract":          {},
	"getcontractcode":       {},
	"getaccountinfo":        {},
	"getstorage":            {},
	"listcontracts":         {},
	"searchlogs":            {},
	"gettransactionreceipt": {},
	"scantxoutset":          {},
	"multichain_rpc":        {},
}

// p2pConsensusMethods return deterministic, security-sensitive chain data.
// When enough peers are connected, the overlay requires identical canonical
// JSON from multiple peers before returning one of these results. Tip- and
// mempool-dependent methods are intentionally excluded because honest nodes can
// legitimately disagree during propagation.
var p2pConsensusMethods = map[string]struct{}{
	"getblockhash":          {},
	"getblock":              {},
	"getblockheader":        {},
	"getrawtransaction":     {},
	"gettxoutproof":         {},
	"verifytxoutproof":      {},
	"getcontractcode":       {},
	"getstorage":            {},
	"gettransactionreceipt": {},
}

func MethodAllowed(method string) bool {
	_, ok := allowedMethods[method]
	return ok
}

func CanTryP2PFirst(method string) bool {
	_, ok := p2pFirstMethods[method]
	return ok
}

func P2PMethodAllowed(method string) bool {
	_, ok := p2pAllowedMethods[method]
	return ok
}

func P2PConsensusRequired(method string) bool {
	_, ok := p2pConsensusMethods[method]
	return ok
}
