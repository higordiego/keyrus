package auth

import "errors"

// Rejection reasons are sentinel values so callers can map them to a transport
// status without parsing text. They are deliberately coarse: a caller must not
// be able to learn from the response which specific check failed.
var (
	ErrTokenMissing        = errors.New("auth: credential is missing")
	ErrTokenMalformed      = errors.New("auth: credential is malformed")
	ErrAlgorithmNotAllowed = errors.New("auth: signing algorithm is not allowed")
	ErrKeyUnknown          = errors.New("auth: signing key is unknown")
	ErrSignatureInvalid    = errors.New("auth: signature is invalid")
	ErrTokenExpired        = errors.New("auth: credential is expired")
	ErrTokenNotYetValid    = errors.New("auth: credential is not valid yet")
	ErrIssuerMismatch      = errors.New("auth: issuer is not trusted")
	ErrAudienceMismatch    = errors.New("auth: audience does not include this service")
	ErrSubjectMissing      = errors.New("auth: subject claim is missing")
	ErrMerchantMissing     = errors.New("auth: merchant claim is missing")
	ErrMerchantForbidden   = errors.New("auth: merchant claim is present on a service identity")
	ErrScopeMissing        = errors.New("auth: required scope is missing")
	ErrOperationUnknown    = errors.New("auth: operation has no declared scope policy")
)

// IsAuthenticationFailure reports whether the credential itself could not be
// established. Authorization failures (scope, tenant) are not included: they
// deserve a different transport status.
func IsAuthenticationFailure(err error) bool {
	switch {
	case errors.Is(err, ErrTokenMissing),
		errors.Is(err, ErrTokenMalformed),
		errors.Is(err, ErrAlgorithmNotAllowed),
		errors.Is(err, ErrKeyUnknown),
		errors.Is(err, ErrSignatureInvalid),
		errors.Is(err, ErrTokenExpired),
		errors.Is(err, ErrTokenNotYetValid),
		errors.Is(err, ErrIssuerMismatch),
		errors.Is(err, ErrAudienceMismatch),
		errors.Is(err, ErrSubjectMissing),
		errors.Is(err, ErrMerchantMissing),
		errors.Is(err, ErrMerchantForbidden):
		return true
	default:
		return false
	}
}

// IsAuthorizationFailure reports whether an established identity was refused.
func IsAuthorizationFailure(err error) bool {
	return errors.Is(err, ErrScopeMissing) || errors.Is(err, ErrOperationUnknown)
}
