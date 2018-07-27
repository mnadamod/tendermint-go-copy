# ADR 014: Secp256k1 Signature Malleability

## Context

Secp256k1 has two layers of malleability.
The signer has a random nonce, and thus can produce many different valid signatures.
This ADR is not concerned with that.
The second layer of malleability basically allows one who is given a signature
to produce exactly one more valid signature for the same message from the same public key.
(They don't even have to know the message!)
The math behind this will be explained in the subsequent section.

Note that in many downstream applications, signatures will appear in a transaction, and therefore in the tx hash.
This means that if someone broadcasts a transaction with secp256k1 signature, the signature can be altered into the other form by anyone in the p2p network.
Thus the tx hash will change, and this altered tx hash may be committed instead.
This breaks the assumption that you can broadcast a valid transaction and just wait for its hash to be included on chain.
You may not even know to increment your sequence number for example.
Removing this second layer of signature malleability concerns could ease downstream development.

### ECDSA context

Secp256k1 is ECDSA over a particular curve.
The signature is of the form `(r, s)`, where `s` is an elliptic curve group element.
However `(r, -s)` is also another valid solution.
Note that anyone can negate a group element, and therefore can get this second signature.

## Decision

We can just distinguish a canonical form for the ECDSA signatures. 
Then we require that all ECDSA signatures be in the canonical form between the two.

The canonical form is rather easy to define and check. 
It would just be the smaller of the two y coordinates for the given x coordinate, defined lexicographically.
Example of other systems using this: https://github.com/zkcrypto/pairing/tree/master/src/bls12_381#serialization.

## Proposed Implementation

Fork https://github.com/btcsuite/btcd, and just update the [parse sig method](https://github.com/btcsuite/btcd/blob/master/btcec/signature.go#195) and serialize functions to enforce our canonical form.

## Status

Proposed.

## Consequences

### Positive
* Lets us maintain the ability to expect a tx hash to appear in the blockchain.

### Negative
* More work in all future implementations (Though this is a very simple check)
* Requires us to maintain another fork

### Neutral
