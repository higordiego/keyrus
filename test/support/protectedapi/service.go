// Package protectedapi is a minimal HTTP adapter that exercises the identity
// boundary of a service: the auth middleware, the scope policy and the tenancy
// guard. It stores nothing but resource ownership and holds no financial rule —
// amounts, dates, idempotent deduplication and balances belong to the ledger and
// consolidation tickets.
package protectedapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/auth/httpauth"
	"github.com/higordiegoti/keyrus/internal/platform/tenancy"
)

// Observation is one request that passed authentication and reached a handler.
type Observation struct {
	Operation  auth.Operation
	MerchantID string
	Headers    http.Header
}

// Service owns the resource directory and the record of what got through.
type Service struct {
	mu            sync.Mutex
	owners        map[string]string
	confirmations int
	observed      []Observation
}

// New builds the service from a resource identifier to owning merchant map.
func New(owners map[string]string) *Service {
	copied := make(map[string]string, len(owners))
	for id, owner := range owners {
		copied[id] = owner
	}
	return &Service{owners: copied}
}

// OwnerOf satisfies tenancy.OwnerResolver over the resource directory.
func (s *Service) OwnerOf(_ context.Context, resourceID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, found := s.owners[resourceID]
	return owner, found, nil
}

// Confirmations counts the mutations that reached the handler. A refused request
// must never increment it.
func (s *Service) Confirmations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.confirmations
}

// Observations returns what the handlers saw.
func (s *Service) Observations() []Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Observation(nil), s.observed...)
}

func (s *Service) observe(operation auth.Operation, identity auth.Identity, request *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observed = append(s.observed, Observation{
		Operation:  operation,
		MerchantID: identity.MerchantID,
		Headers:    request.Header.Clone(),
	})
}

// Handler wires the production middleware in front of the operation's handler.
func (s *Service) Handler(verifier *auth.Verifier, operation auth.Operation) (http.Handler, error) {
	middleware, err := httpauth.Middleware(httpauth.Config{
		Verifier:  verifier,
		Policy:    auth.PublicEdgePolicy(),
		Operation: operation,
	})
	if err != nil {
		return nil, err
	}
	inner, err := s.operationHandler(operation)
	if err != nil {
		return nil, err
	}
	return middleware(inner), nil
}

func (s *Service) operationHandler(operation auth.Operation) (http.Handler, error) {
	switch operation {
	case auth.OperationCreateEntry, auth.OperationCreateReversal:
		return http.HandlerFunc(s.create), nil
	case auth.OperationGetEntry:
		return http.HandlerFunc(s.get), nil
	case auth.OperationListEntries:
		return http.HandlerFunc(s.list), nil
	default:
		return nil, errUnsupported(operation)
	}
}

// create records that a command got through. It writes no financial value.
func (s *Service) create(writer http.ResponseWriter, request *http.Request) {
	identity, authenticated := auth.IdentityFrom(request.Context())
	if !authenticated {
		http.Error(writer, "no identity", http.StatusInternalServerError)
		return
	}
	s.observe(auth.OperationCreateEntry, identity, request)

	s.mu.Lock()
	s.confirmations++
	s.mu.Unlock()

	writeJSON(writer, http.StatusCreated, map[string]string{"merchant_id": identity.MerchantID})
}

// get resolves one resource through the tenancy guard, so a resource owned by
// another merchant is answered exactly like one that does not exist.
func (s *Service) get(writer http.ResponseWriter, request *http.Request) {
	identity, authenticated := auth.IdentityFrom(request.Context())
	if !authenticated {
		http.Error(writer, "no identity", http.StatusInternalServerError)
		return
	}
	s.observe(auth.OperationGetEntry, identity, request)

	guard, err := tenancy.NewGuard(s)
	if err != nil {
		http.Error(writer, "guard unavailable", http.StatusInternalServerError)
		return
	}
	resourceID := strings.TrimPrefix(request.URL.Path, "/v1/entries/")
	if err := guard.Authorize(request.Context(), identity, resourceID); err != nil {
		httpauth.WriteResourceUnavailable(writer)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"entry_id": resourceID})
}

// list returns only the identifiers the authenticated merchant owns.
func (s *Service) list(writer http.ResponseWriter, request *http.Request) {
	identity, authenticated := auth.IdentityFrom(request.Context())
	if !authenticated {
		http.Error(writer, "no identity", http.StatusInternalServerError)
		return
	}
	s.observe(auth.OperationListEntries, identity, request)
	writeJSON(writer, http.StatusOK, map[string]any{"entry_ids": s.VisibleTo(identity)})
}

// VisibleTo returns the resources the identity owns, in a stable order.
func (s *Service) VisibleTo(identity auth.Identity) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var visible []string
	for id, owner := range s.owners {
		if identity.Owns(owner) {
			visible = append(visible, id)
		}
	}
	sort.Strings(visible)
	return visible
}

func writeJSON(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}

type unsupportedOperation auth.Operation

func (e unsupportedOperation) Error() string {
	return "protectedapi: operation " + string(e) + " has no handler"
}

func errUnsupported(operation auth.Operation) error { return unsupportedOperation(operation) }
