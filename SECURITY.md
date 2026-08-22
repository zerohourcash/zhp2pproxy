# Security policy

## Supported versions

Security fixes are provided for the latest published release. Operators should
keep automatic update checks enabled or monitor official release notices.

## Reporting a vulnerability

Do not open a public issue for an undisclosed vulnerability. Use the official
repository's private vulnerability reporting / Security Advisory form and
include:

- affected version and operating system;
- impact and required attacker capabilities;
- minimal reproduction steps or a proof of concept;
- relevant logs with credentials, tokens, private keys and IPs removed.

The maintainers will acknowledge a complete report, reproduce it privately,
prepare a coordinated fix and publish an advisory after patched artifacts are
available. Never test against third-party nodes or users without permission.

## Secrets that must never enter Git

The following files are runtime state, not source code:

- `proxy.json` and timestamped backups;
- `identity.key`, overlay PSKs and signing keys;
- `peers.json` when peer addresses are operationally sensitive;
- `zerohour.conf`, RPC cookies, usernames and passwords;
- `.env`, certificate private keys, PFX/P12 files and service logs.

Release signing keys are stored offline/outside the repository. If a secret is
committed, removing the file in a later commit is insufficient: rotate the
secret immediately and purge it from history before publication.

## Security boundaries

ZHP2PProxy does not handle wallet private keys and does not sign transactions.
It accepts already signed raw transactions. Overlay peers and browser clients
are untrusted. The shared deterministic overlay PSK separates protocols but is
not access control. See [THREAT_MODEL.md](THREAT_MODEL.md) for the full model.

Checksums protect against corruption. Authenticity requires the Ed25519-signed
release metadata and, on Windows, a valid Authenticode signature. HTTPS alone
or a checksum downloaded from the same server is not a sufficient trust root.
