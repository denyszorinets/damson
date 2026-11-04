// Package secrets holds the secret reference grammar, the secret type table
// and the envelope encryption of the db backend (ARCHITECTURE 9). The root
// package is pure: no I/O, randomness is injected. The backend that talks to
// the store lives in secrets/db.
package secrets
