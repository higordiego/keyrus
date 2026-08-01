package auth

import "sort"

// Operation names a protected surface. HTTP uses "METHOD /path"; gRPC uses the
// full method name.
type Operation string

// Public HTTP operations exposed through the KrakenD edge.
const (
	OperationCreateEntry        Operation = "POST /v1/entries"
	OperationCreateReversal     Operation = "POST /v1/entries/{entry_id}/reversals"
	OperationGetEntry           Operation = "GET /v1/entries/{entry_id}"
	OperationListEntries        Operation = "GET /v1/entries"
	OperationGetDailyBalances   Operation = "GET /v1/daily-balances"
	OperationGetWatermark       Operation = "/cashflow.ledger.internal.v1.LedgerInternalService/GetMerchantWatermark"
	OperationStreamEntriesAtCut Operation = "/cashflow.ledger.internal.v1.LedgerInternalService/StreamEntriesAtCut"
)

// Scopes granted by the identity provider.
const (
	ScopeLedgerRead        = "ledger:read"
	ScopeLedgerWrite       = "ledger:write"
	ScopeConsolidationRead = "consolidation:read"
	ScopeLedgerInternal    = "ledger:internal:read"
	ScopeOpsReconcile      = "ops:reconcile"
)

// ScopePolicy maps each protected operation to the scopes it requires. It is a
// closed set: an operation absent from the policy is refused, so adding a route
// without declaring its scope fails instead of silently exposing it.
type ScopePolicy map[Operation][]string

// PublicEdgePolicy is the authorization policy the HTTP adapters enforce again
// after the edge has already validated the same token.
func PublicEdgePolicy() ScopePolicy {
	return ScopePolicy{
		OperationCreateEntry:      {ScopeLedgerWrite},
		OperationCreateReversal:   {ScopeLedgerWrite},
		OperationGetEntry:         {ScopeLedgerRead},
		OperationListEntries:      {ScopeLedgerRead},
		OperationGetDailyBalances: {ScopeConsolidationRead},
	}
}

// InternalGRPCPolicy is the authorization policy of the private gRPC surface.
func InternalGRPCPolicy() ScopePolicy {
	return ScopePolicy{
		OperationGetWatermark:       {ScopeLedgerInternal},
		OperationStreamEntriesAtCut: {ScopeLedgerInternal, ScopeOpsReconcile},
	}
}

// Authorize refuses the identity unless the operation is declared and every
// scope it requires was granted.
func (p ScopePolicy) Authorize(operation Operation, identity Identity) error {
	required, declared := p[operation]
	if !declared {
		return ErrOperationUnknown
	}
	if !identity.Scopes.HasAll(required...) {
		return ErrScopeMissing
	}
	return nil
}

// Operations lists the declared operations in a stable order.
func (p ScopePolicy) Operations() []Operation {
	names := make([]Operation, 0, len(p))
	for operation := range p {
		names = append(names, operation)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}
