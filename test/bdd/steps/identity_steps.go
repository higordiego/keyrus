package steps

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
)

// timingSamples and the tolerance below make the enumeration check a smoke
// test, not a cryptographic proof: it catches a lookup that short-circuits on an
// absent identifier, and it is deliberately generous so ordinary scheduler noise
// on a shared runner does not produce a false failure.
const (
	timingSamples        = 64
	timingFloor          = 300 * time.Microsecond
	timingRelativeMargin = 0.5
)

func (w *world) givenValidMerchantCredential() error {
	token, err := w.mintValid()
	if err != nil {
		return err
	}
	w.token = token
	w.tokenCondition = "válido"
	return nil
}

func (w *world) whenAuthorizedOperationIsRequested() error {
	response, body, err := w.callService(auth.OperationListEntries, http.MethodGet, "/v1/entries", nil)
	if err != nil {
		return err
	}
	w.response, w.responseBody = response, body
	return nil
}

func (w *world) thenMerchantIsDerivedFromTheIdentity() error {
	if w.response.StatusCode != http.StatusOK {
		return fmt.Errorf("a valid credential was refused with status %d", w.response.StatusCode)
	}
	observations := w.service.Observations()
	if len(observations) != 1 {
		return fmt.Errorf("expected exactly one authenticated request, got %d", len(observations))
	}
	if observations[0].MerchantID != merchantA {
		return fmt.Errorf("merchant derived from the identity is %q, want %q", observations[0].MerchantID, merchantA)
	}

	if header := observations[0].Headers.Get("X-Merchant-Id"); header != "" {
		return fmt.Errorf("a caller supplied merchant header survived into the handler: %q", header)
	}
	return nil
}

func (w *world) thenOperationStaysScopedToThatMerchant() error {
	payload := string(w.responseBody)
	if !strings.Contains(payload, resourceOfA) {
		return fmt.Errorf("the merchant's own resource is missing from the response: %s", payload)
	}
	if strings.Contains(payload, resourceOfB) {
		return fmt.Errorf("the response leaked a resource owned by another merchant: %s", payload)
	}
	return nil
}

func (w *world) givenCredentialCondition(condition string) error {
	token, err := w.mintCondition(condition)
	if err != nil {
		return err
	}
	w.token, w.tokenCondition = token, condition
	return nil
}

func (w *world) whenProtectedOperationIsRequested() error {
	response, body, err := w.callService(auth.OperationCreateEntry, http.MethodPost, "/v1/entries", nil)
	if err != nil {
		return err
	}
	w.response, w.responseBody = response, body
	return nil
}

func (w *world) thenRequestIsRejected() error {
	switch w.response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil
	default:
		return fmt.Errorf("credential %q was answered with status %d instead of a refusal", w.tokenCondition, w.response.StatusCode)
	}
}

func (w *world) thenNoFinancialDataChangedOrLeaked() error {
	if confirmations := w.service.Confirmations(); confirmations != 0 {
		return fmt.Errorf("a refused request confirmed %d commands", confirmations)
	}
	if observations := w.service.Observations(); len(observations) != 0 {
		return fmt.Errorf("a refused request reached the handler %d times", len(observations))
	}
	for _, leaked := range []string{resourceOfA, resourceOfB, merchantA, merchantB} {
		if bytes.Contains(w.responseBody, []byte(leaked)) {
			return fmt.Errorf("the refusal body revealed %q: %s", leaked, w.responseBody)
		}
	}
	return nil
}

func (w *world) givenResourceBelongsToAnotherMerchant() error {
	token, err := w.mintValid(auth.ScopeLedgerRead)
	if err != nil {
		return err
	}
	w.token = token
	owner, found, err := w.service.OwnerOf(context.Background(), resourceOfB)
	if err != nil {
		return err
	}
	if !found || owner != merchantB {
		return fmt.Errorf("fixture is wrong: %q is owned by %q (found=%t)", resourceOfB, owner, found)
	}
	return nil
}

func (w *world) whenAuthenticatedIdentityTriesToReachIt() error {
	foreign, foreignBody, err := w.callService(auth.OperationGetEntry, http.MethodGet, "/v1/entries/"+resourceOfB, nil)
	if err != nil {
		return err
	}
	absent, absentBody, err := w.callService(auth.OperationGetEntry, http.MethodGet, "/v1/entries/"+absentID, nil)
	if err != nil {
		return err
	}
	w.response, w.responseBody = foreign, foreignBody
	w.baselineStatus, w.baselineBody = absent.StatusCode, absentBody

	verdict, err := w.compareRefusalTiming()
	if err != nil {
		return err
	}
	w.timingVerdict = verdict
	return nil
}

func (w *world) thenOperationIsDenied() error {
	if w.response.StatusCode == http.StatusOK {
		return fmt.Errorf("a resource owned by another merchant was served")
	}
	if w.response.StatusCode != http.StatusNotFound {
		return fmt.Errorf("horizontal access answered with status %d, want %d", w.response.StatusCode, http.StatusNotFound)
	}
	return nil
}

func (w *world) thenResponseHidesExistence() error {
	for _, revealing := range []string{resourceOfB, merchantB, "forbidden", "not owned", "exists"} {
		if bytes.Contains(bytes.ToLower(w.responseBody), bytes.ToLower([]byte(revealing))) {
			return fmt.Errorf("the refusal hinted at the resource with %q: %s", revealing, w.responseBody)
		}
	}
	return nil
}

func (w *world) thenStatusAndBodyMatchAnAbsentIdentifier() error {
	if w.response.StatusCode != w.baselineStatus {
		return fmt.Errorf("status differs: foreign resource %d, absent identifier %d", w.response.StatusCode, w.baselineStatus)
	}
	if !bytes.Equal(w.responseBody, w.baselineBody) {
		return fmt.Errorf("body differs:\n foreign: %s absent:  %s", w.responseBody, w.baselineBody)
	}
	return nil
}

func (w *world) thenTimingDoesNotEnableEnumeration() error {
	if w.timingVerdict != "" {
		return fmt.Errorf("%s", w.timingVerdict)
	}
	return nil
}

// compareRefusalTiming interleaves both refusal paths and compares their median
// duration. It returns an empty string when the two are indistinguishable within
// the documented tolerance.
func (w *world) compareRefusalTiming() (string, error) {
	foreign := make([]time.Duration, 0, timingSamples)
	absent := make([]time.Duration, 0, timingSamples)

	for range timingSamples {
		foreignSample, err := w.measureRefusal(resourceOfB)
		if err != nil {
			return "", err
		}
		absentSample, err := w.measureRefusal(absentID)
		if err != nil {
			return "", err
		}
		foreign = append(foreign, foreignSample)
		absent = append(absent, absentSample)
	}

	foreignMedian, absentMedian := median(foreign), median(absent)
	difference := foreignMedian - absentMedian
	if difference < 0 {
		difference = -difference
	}
	larger := max(foreignMedian, absentMedian)

	tolerance := time.Duration(float64(larger) * timingRelativeMargin)
	if tolerance < timingFloor {
		tolerance = timingFloor
	}
	if difference > tolerance {
		return fmt.Sprintf(
			"refusal timing is distinguishable: foreign median %v, absent median %v, difference %v above the %v tolerance",
			foreignMedian, absentMedian, difference, tolerance,
		), nil
	}
	return "", nil
}

func (w *world) measureRefusal(resourceID string) (time.Duration, error) {
	started := time.Now()
	response, _, err := w.callService(auth.OperationGetEntry, http.MethodGet, "/v1/entries/"+resourceID, nil)
	elapsed := time.Since(started)
	if err != nil {
		return 0, err
	}
	if response.StatusCode != http.StatusNotFound {
		return 0, fmt.Errorf("timing sample for %q returned status %d", resourceID, response.StatusCode)
	}
	return elapsed, nil
}

func median(samples []time.Duration) time.Duration {
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}
