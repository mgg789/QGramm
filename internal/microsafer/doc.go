//go:build qg_ai_endpoint && qg_e2ee

// Package microsafer is a small, external MLS sidecar for an AI endpoint.
//
// The package deliberately has no dependency on QGramm core or its runtime
// configuration.  It owns one separately provisioned user/device identity,
// an encrypted local SQLite state database and an authenticated HTTP relay.
// All MLS state transitions are snapshotted before their wire output is
// released.  A process lock prevents two owners from advancing the same
// ratchet concurrently.
package microsafer
