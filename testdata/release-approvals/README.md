# Ephemeral approval-receipt fixtures

`scripts/release/test-remote-gates.sh` creates and signs the following receipt
fixtures in a throwaway GPG home: valid, missing-signature, wrong-fingerprint,
noncanonical-JSON, expired, replayed-nonce, target-after-signature-mutation,
public-visibility, and mutable-image. No private key or production receipt is
stored in this repository.
