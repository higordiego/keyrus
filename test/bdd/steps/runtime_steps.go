package steps

func (w *world) givenValidIdentityEvidence() error      { return w.require("valid_identity") }
func (w *world) whenAuthorizedOperationEvidence() error { return w.require("authorized_operation") }
func (w *world) thenMerchantDerivedEvidence() error     { return w.require("merchant_derived") }
func (w *world) thenTenantLimitedEvidence() error       { return w.require("tenant_limited") }

func (w *world) givenTokenConditionEvidence(condition string) error {
	return w.requireCondition(condition)
}
func (w *world) whenProtectedOperationEvidence() error { return w.require("protected_operation") }
func (w *world) thenOperationRejectedEvidence() error  { return w.require("rejected") }
func (w *world) thenNoEffectOrDisclosureEvidence() error {
	return w.require("no_effect_or_disclosure")
}

func (w *world) givenForeignResourceEvidence() error { return w.require("foreign_resource") }
func (w *world) whenHorizontalAccessEvidence() error { return w.require("access_attempted") }
func (w *world) thenHorizontalDeniedEvidence() error { return w.require("denied") }
func (w *world) thenExistenceHiddenEvidence() error  { return w.require("existence_hidden") }
func (w *world) thenContractEqualEvidence() error    { return w.require("contract_equal") }
func (w *world) thenTimingSafeEvidence() error       { return w.require("timing_indistinguishable") }

func (w *world) givenJWTConditionEvidence(condition string) error {
	return w.requireCondition(condition)
}
func (w *world) whenPublicEdgeCalledEvidence() error { return w.require("public_edge_call") }
func (w *world) thenEdgeRejectedWithoutForwardEvidence() error {
	return w.require("rejected_without_forward")
}

func (w *world) givenDirectPrivateCallEvidence() error { return w.require("direct_private_call") }
func (w *world) givenInvalidOperationJWTEvidence() error {
	return w.require("invalid_operation_jwt")
}
func (w *world) whenServiceValidatesEvidence() error { return w.require("service_validated") }
func (w *world) thenNoCommitEvidence() error         { return w.require("no_commit") }

func (w *world) givenFourHeadersEvidence() error  { return w.require("four_headers_sent") }
func (w *world) whenEdgeForwardedEvidence() error { return w.require("edge_forwarded") }
func (w *world) thenFourHeadersPreservedEvidence() error {
	return w.require("four_headers_preserved")
}

func (w *world) givenCommitThenEOFEvidence() error { return w.require("commit_then_eof") }
func (w *world) whenEdgeObservedFailureEvidence() error {
	return w.require("edge_observed_failure")
}
func (w *world) thenSingleGatewayInvocationEvidence() error {
	return w.require("single_gateway_invocation")
}
func (w *world) thenIdempotentClientReplayEvidence() error {
	return w.require("idempotent_client_replay")
}

func (w *world) givenKeycloakInternalEvidence() error  { return w.require("keycloak_internal") }
func (w *world) whenExternalProbeEvidence() error      { return w.require("external_probe") }
func (w *world) thenPrivatePathsAbsentEvidence() error { return w.require("private_paths_absent") }
func (w *world) thenPublicOIDCOnlyEvidence() error     { return w.require("public_oidc_only") }

func (w *world) givenWatermarkInternalEvidence() error { return w.require("watermark_internal") }
func (w *world) whenMissingServiceIdentityEvidence() error {
	return w.require("missing_service_identity")
}
func (w *world) thenLedgerRejectedEvidence() error { return w.require("ledger_rejected") }
func (w *world) thenNoPublicRouteEvidence() error  { return w.require("no_public_route") }

func (w *world) givenContextAndDeadlineEvidence() error { return w.require("context_and_deadline") }
func (w *world) whenCrossedGRPCEvidence() error         { return w.require("crossed_grpc") }
func (w *world) thenTraceparentCorrelatedEvidence() error {
	return w.require("traceparent_correlated")
}
func (w *world) thenLimitsEnforcedEvidence() error { return w.require("limits_enforced") }
func (w *world) thenTelemetryRedactedEvidence() error {
	return w.require("telemetry_redacted")
}
