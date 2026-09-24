# Threat model

## Assets

- availability and integrity of RPC answers;
- local ZHCASH RPC credentials and node configuration;
- proxy identity and private overlay PSKs;
- release signing keys and update integrity;
- host resources, network access and service privileges.

Wallet seed phrases and private keys are outside the proxy boundary. The proxy
must never request, store or log them.

## Untrusted inputs

Treat HTTP/WebSocket/WebRTC clients, libp2p peers, pubsub messages, bootstrap
documents, JSON-RPC parameters, native node traffic and release servers as
potentially hostile. A peer ID proves possession of that peer's key; it does
not prove that the operator is trusted or that an RPC answer is correct.

The deterministic PSK derived from the public network ID prevents accidental
cross-network traffic but is public knowledge. It is not membership security.
Private deployments must distribute a random PSK through a separate channel.

## Network exposure

The default HTTP API and native node tunnel bind to loopback. Public operators
should expose the libp2p and browser transports, not the unrestricted root HTTP
RPC endpoint. If HTTP must be remote, require an API token, an IP allowlist and
a TLS reverse proxy. The process refuses a non-loopback HTTP listener unless
`api_token` or `allowed_ips` is configured. CORS is a browser policy, not
client authentication.

RPC methods are deny-by-default at the overlay/browser boundary. Expensive
scans have descriptor validation, global serialization and rate limits. Node
control, wallet signing and arbitrary destination tunnelling are prohibited.

Adaptive NAT traversal does not expose the local RPC or native tunnel. DCUtR
upgrades an already authenticated libp2p relay connection, and periodic direct
redials accept only literal public IP multiaddrs; private, loopback and
peer-controlled DNS targets are rejected. UPnP/NAT-PMP port mapping remains
disabled by default. Relay service is a separate operator role with reservation,
circuit, byte and time limits; ordinary client templates do not enable it.

## RPC answer integrity

Remote RPC answers are availability fallbacks, not cryptographic proofs. A
malicious peer can lie about balances, transactions, UTXOs, logs or chain tip.
Callers must validate transaction structure, block linkage and proof-bearing
responses, and should compare sensitive reads with an independent peer or
trusted HTTPS RPC. A response must never cause a wallet to disclose a secret or
sign data other than the transaction the user reviewed.

## Updates

The embedded Ed25519 public key verifies release metadata. The private key is
kept outside Git. Production code accepts update manifests and binary URLs only
from GitHub Releases in `zerohourcash/zhp2pproxy`; redirects are limited to
GitHub's release asset hosts. The manifest carries a monotonic sequence to
prevent rollback and SHA-256 digests to bind artifacts. Linux activation is confined to the
executable directory, verifies the downloaded binary's version and keeps one
rollback copy. Windows public artifacts additionally require Authenticode.

The public release process refuses dirty worktrees, unsigned metadata and
unsigned Windows publication. A compromised download server alone must not be
able to produce a trusted release.

## Host compromise

An attacker who can write the proxy installation directory or run as the proxy
service account can replace its runtime state. An attacker with root/SYSTEM
already controls the process. Linux deployments therefore use an unprivileged
account and the hardened systemd unit wherever possible. Runtime files are
owner-only and filesystem operations reject escaping symlinks.

## Explicit non-goals

- anonymity against peers, relays or network observers;
- consensus replacement or independent validation of the ZHCASH chain;
- protection after root/SYSTEM or release signing-key compromise;
- guaranteed direct connectivity between every pair of NAT types.
