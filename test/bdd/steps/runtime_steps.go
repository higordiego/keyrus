package steps

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/observability/tracecontext"
	"github.com/higordiegoti/keyrus/test/support/timingoracle"
)

// Every binding below reads the values the real stack observed. A step never
// accepts a bare "passed" flag: it names the oracle, the observation keys and,
// wherever the outcome has one correct value, that exact value.

func (w *world) givenValidIdentityEvidence() error {
	return w.requireValues("valid_identity", map[string]string{
		"issuer":      issuerURL,
		"audience":    publicAudience,
		"merchant_id": merchantA,
		"scope":       "ledger:read",
	})
}

func (w *world) whenAuthorizedOperationEvidence() error {
	return w.requireValues("authorized_operation", map[string]string{"edge_status": "200"})
}

func (w *world) thenMerchantDerivedEvidence() error {
	return w.requireValues("merchant_derived", map[string]string{
		"merchant_id": merchantA,
		"entry_id":    resourceOfA,
	})
}

func (w *world) thenTenantLimitedEvidence() error {
	return w.requireValues("tenant_limited", map[string]string{"cross_status": "404", "own_status": "200"})
}

func (w *world) givenTokenConditionEvidence(condition string) error {
	return w.requireCondition(condition)
}

func (w *world) whenProtectedOperationEvidence() error {
	return w.requireValues("protected_operation", map[string]string{"path": "/v1/entries"})
}

// thenOperationRejectedEvidence is shared by the edge examples and by the direct
// private call, which record the rejection under different observation keys.
func (w *world) thenOperationRejectedEvidence() error {
	if err := w.require("rejected"); err != nil {
		return err
	}
	for _, key := range []string{"edge_status", "status"} {
		observed, err := w.observation("rejected", key)
		if err != nil || observed == "" {
			continue
		}
		if observed != "401" && observed != "403" {
			return fmt.Errorf("rejection status %s=%q is not an authentication or authorization refusal", key, observed)
		}
		return nil
	}
	return fmt.Errorf("oracle rejected carries no observed status")
}

func (w *world) thenNoEffectOrDisclosureEvidence() error {
	return w.requireValues("no_effect_or_disclosure", map[string]string{
		"entrypoint_delta":      "0",
		"authenticated_delta":   "0",
		"identifiers_disclosed": "none",
	})
}

func (w *world) givenForeignResourceEvidence() error {
	return w.requireValues("foreign_resource", map[string]string{
		"entry_id":          resourceOfB,
		"owner_merchant_id": merchantB,
	})
}

func (w *world) whenHorizontalAccessEvidence() error {
	return w.requireValues("access_attempted", map[string]string{
		"caller_merchant_id": merchantA,
		"path":               "/v1/entries/" + resourceOfB,
	})
}

func (w *world) thenHorizontalDeniedEvidence() error {
	return w.requireValues("denied", map[string]string{"status": "404"})
}

func (w *world) thenExistenceHiddenEvidence() error {
	return w.requireValues("existence_hidden", map[string]string{"identifiers_disclosed": "none"})
}

func (w *world) thenContractEqualEvidence() error {
	return w.requireValues("contract_equal", map[string]string{"cross_status": "404", "absent_status": "404"})
}

// thenTimingSafeEvidence re-decides the anti-enumeration verdict from the
// measured populations instead of trusting a recorded conclusion.
func (w *world) thenTimingSafeEvidence() error {
	difference, err := w.observedDuration("timing_indistinguishable", "difference")
	if err != nil {
		return err
	}
	tolerance, err := w.observedDuration("timing_indistinguishable", "tolerance")
	if err != nil {
		return err
	}
	rawSeparability, err := w.observation("timing_indistinguishable", "separability")
	if err != nil {
		return err
	}
	separability, err := strconv.ParseFloat(rawSeparability, 64)
	if err != nil {
		return fmt.Errorf("observed separability %q is not a number: %w", rawSeparability, err)
	}
	if difference > tolerance {
		return fmt.Errorf("observed median gap %s exceeds the declared tolerance %s", difference, tolerance)
	}
	if separability > timingoracle.MaxSeparability {
		return fmt.Errorf("observed rank separability %.3f allows practical enumeration", separability)
	}
	return nil
}

func (w *world) givenJWTConditionEvidence(condition string) error {
	return w.requireCondition(condition)
}

func (w *world) whenPublicEdgeCalledEvidence() error {
	return w.requireValues("public_edge_call", map[string]string{"path": "/v1/entries"})
}

// thenEdgeRejectedWithoutForwardEvidence compares the Ledger's pre-authentication
// entry counter around the call. That counter moves for every request that
// reaches the service, including the ones its own middleware then refuses, so an
// unchanged value proves the edge never forwarded the call.
func (w *world) thenEdgeRejectedWithoutForwardEvidence() error {
	before, err := w.observation("rejected_without_forward", "entrypoint_before")
	if err != nil {
		return err
	}
	after, err := w.observation("rejected_without_forward", "entrypoint_after")
	if err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("the edge forwarded the rejected call: Ledger entry counter moved from %s to %s", before, after)
	}
	control, err := w.observation("rejected_without_forward", "forwarding_control")
	if err != nil {
		return err
	}
	if !strings.Contains(control, "detected") {
		return fmt.Errorf("the run did not prove the counter detects forwarding: %q", control)
	}
	return nil
}

func (w *world) givenDirectPrivateCallEvidence() error {
	return w.requireValues("direct_private_call", map[string]string{"bypassed": "krakend"})
}

func (w *world) givenInvalidOperationJWTEvidence() error {
	return w.require("invalid_operation_jwt")
}

// whenServiceValidatesEvidence requires the opposite of the edge oracle: the
// direct call did reach the Ledger, so the entry counter must have moved.
func (w *world) whenServiceValidatesEvidence() error {
	return w.requireValues("service_validated", map[string]string{"entrypoint_delta": "1"})
}

func (w *world) thenNoCommitEvidence() error {
	return w.requireValues("no_commit", map[string]string{"authenticated_delta": "0"})
}

func (w *world) givenFourHeadersEvidence() error {
	return w.requireValues("four_headers_sent", map[string]string{"tracestate": tracecontext.PublicTraceState})
}

func (w *world) whenEdgeForwardedEvidence() error {
	return w.requireValues("edge_forwarded", map[string]string{"backend_invocations": "1"})
}

// thenFourHeadersPreservedEvidence compares what the backend saw against what
// the client sent, header by header.
func (w *world) thenFourHeadersPreservedEvidence() error {
	sent, err := w.observation("four_headers_sent", "authorization_sha256")
	if err != nil {
		return err
	}
	for _, key := range []string{"authorization_sha256", "idempotency_key", "tracestate"} {
		origin, err := w.observation("four_headers_sent", key)
		if err != nil {
			return err
		}
		received, err := w.observation("four_headers_preserved", key)
		if err != nil {
			return err
		}
		if origin != received {
			return fmt.Errorf("backend observed %s=%q while the client sent %q", key, received, origin)
		}
	}
	traceParent, err := w.observation("four_headers_sent", "traceparent")
	if err != nil {
		return err
	}
	traceID, err := w.observation("four_headers_preserved", "trace_id")
	if err != nil {
		return err
	}
	if !strings.Contains(traceParent, traceID) {
		return fmt.Errorf("backend trace ID %q does not belong to the caller traceparent %q", traceID, traceParent)
	}
	if sent == "" {
		return fmt.Errorf("no authorization digest was observed")
	}
	return nil
}

func (w *world) givenCommitThenEOFEvidence() error {
	return w.requireValues("commit_then_eof", map[string]string{"commits": "1", "invocations": "1", "replays": "0"})
}

func (w *world) whenEdgeObservedFailureEvidence() error {
	return w.require("edge_observed_failure")
}

func (w *world) thenSingleGatewayInvocationEvidence() error {
	return w.requireValues("single_gateway_invocation", map[string]string{"invocations": "1", "commits": "1"})
}

func (w *world) thenIdempotentClientReplayEvidence() error {
	return w.requireValues("idempotent_client_replay", map[string]string{
		"invocations":   "2",
		"commits":       "1",
		"replays":       "1",
		"replay_status": "200",
	})
}

func (w *world) givenKeycloakInternalEvidence() error {
	return w.requireValues("keycloak_internal", map[string]string{"keycloak_network_alias": "keycloak"})
}

// whenExternalProbeEvidence pins the health probe path itself: the container is
// healthy while the very path its probe uses answers 404 on the published port.
func (w *world) whenExternalProbeEvidence() error {
	return w.requireValues("external_probe", map[string]string{
		"health_probe_status": "404",
		"container_health":    "healthy",
	})
}

func (w *world) thenPrivatePathsAbsentEvidence() error {
	statuses, err := w.observation("private_paths_absent", "statuses")
	if err != nil {
		return err
	}
	for _, status := range strings.Split(statuses, ",") {
		if strings.TrimSpace(status) != "404" {
			return fmt.Errorf("a private path answered %q instead of 404: %s", status, statuses)
		}
	}
	return nil
}

func (w *world) thenPublicOIDCOnlyEvidence() error {
	paths, err := w.observation("public_oidc_only", "public_oidc_paths")
	if err != nil {
		return err
	}
	for _, path := range strings.Split(paths, ",") {
		if !strings.HasPrefix(strings.TrimSpace(path), "/realms/cashflow/") {
			return fmt.Errorf("public OIDC surface contains a foreign path %q", path)
		}
	}
	return nil
}

func (w *world) givenWatermarkInternalEvidence() error {
	return w.requireValues("watermark_internal", map[string]string{"transport": "grpc+mtls"})
}

func (w *world) whenMissingServiceIdentityEvidence() error {
	return w.requireValues("missing_service_identity", map[string]string{"authorization_metadata": "absent"})
}

func (w *world) thenLedgerRejectedEvidence() error {
	return w.requireValues("ledger_rejected", map[string]string{"grpc_code": "Unauthenticated"})
}

func (w *world) thenNoPublicRouteEvidence() error {
	endpoints, err := w.observation("no_public_route", "config_endpoints")
	if err != nil {
		return err
	}
	for _, endpoint := range strings.Split(endpoints, ",") {
		lower := strings.ToLower(strings.TrimSpace(endpoint))
		if strings.Contains(lower, "watermark") || strings.Contains(lower, "internal") || strings.Contains(lower, "grpc") {
			return fmt.Errorf("the running edge config exposes a private route %q", endpoint)
		}
	}
	return w.requireValues("no_public_route", map[string]string{"probe_statuses": "404,404,404"})
}

func (w *world) givenContextAndDeadlineEvidence() error {
	return w.require("context_and_deadline")
}

func (w *world) whenCrossedGRPCEvidence() error {
	return w.requireValues("crossed_grpc", map[string]string{"span_kinds": "Server,Client,Server"})
}

func (w *world) thenTraceparentCorrelatedEvidence() error {
	if err := w.requireValues("traceparent_correlated", map[string]string{
		"lineage": "http-server -> grpc-client -> grpc-server",
	}); err != nil {
		return err
	}
	traceID, err := w.observation("traceparent_correlated", "trace_id")
	if err != nil {
		return err
	}
	crossed, err := w.observation("crossed_grpc", "trace_id")
	if err != nil {
		return err
	}
	if traceID != crossed {
		return fmt.Errorf("the gRPC hop reported trace %q while the correlation oracle reported %q", crossed, traceID)
	}
	return nil
}

func (w *world) thenLimitsEnforcedEvidence() error {
	return w.requireValues("limits_enforced", map[string]string{
		"deadline_code": "DeadlineExceeded",
		"cancel_code":   "Canceled",
		"oversize_code": "ResourceExhausted",
	})
}

func (w *world) thenTelemetryRedactedEvidence() error {
	return w.requireValues("telemetry_redacted", map[string]string{"matches": "0"})
}

func (w *world) observedDuration(oracle, key string) (time.Duration, error) {
	raw, err := w.observation(oracle, key)
	if err != nil {
		return 0, err
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("observed %s=%q is not a duration: %w", key, raw, err)
	}
	return parsed, nil
}
