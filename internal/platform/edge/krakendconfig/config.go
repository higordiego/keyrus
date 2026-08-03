// Package krakendconfig loads the versioned KrakenD Community configuration and
// proves the edge invariants that the architecture depends on: only the public
// contract is routable, every protected route validates a JWT, headers are
// allow-listed rather than forwarded wholesale, and no command is retried.
package krakendconfig

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config is the subset of the KrakenD schema this project asserts on. Unknown
// fields stay in Raw so an invariant can still inspect them.
type Config struct {
	Version     int                        `json:"version"`
	Name        string                     `json:"name"`
	Port        int                        `json:"port"`
	Timeout     string                     `json:"timeout"`
	ExtraConfig map[string]json.RawMessage `json:"extra_config"`
	Endpoints   []Endpoint                 `json:"endpoints"`

	Raw json.RawMessage `json:"-"`
}

// Endpoint is one routable method and path pair.
type Endpoint struct {
	Path              string                     `json:"endpoint"`
	Method            string                     `json:"method"`
	OutputEncoding    string                     `json:"output_encoding"`
	Timeout           string                     `json:"timeout"`
	InputHeaders      []string                   `json:"input_headers"`
	InputQueryStrings []string                   `json:"input_query_strings"`
	ExtraConfig       map[string]json.RawMessage `json:"extra_config"`
	Backend           []Backend                  `json:"backend"`

	Raw json.RawMessage `json:"-"`
}

// Backend is one upstream declared for an endpoint.
type Backend struct {
	URLPattern  string                     `json:"url_pattern"`
	Method      string                     `json:"method"`
	Encoding    string                     `json:"encoding"`
	Host        []string                   `json:"host"`
	ExtraConfig map[string]json.RawMessage `json:"extra_config"`
}

// JWTValidator is the KrakenD auth/validator block.
type JWTValidator struct {
	Algorithm          string   `json:"alg"`
	JWKURL             string   `json:"jwk_url"`
	Issuer             string   `json:"issuer"`
	Audience           []string `json:"audience"`
	Scopes             []string `json:"scopes"`
	ScopesKey          string   `json:"scopes_key"`
	ScopesMatcher      string   `json:"scopes_matcher"`
	DisableJWKSecurity bool     `json:"disable_jwk_security"`
	OperationDebug     bool     `json:"operation_debug"`
}

// ValidatorNamespace is the KrakenD extra_config key holding the JWT policy.
const ValidatorNamespace = "auth/validator"

// Route is the "METHOD path" identifier used by the policy.
func (e Endpoint) Route() string {
	return e.Method + " " + e.Path
}

// Validator returns the endpoint JWT policy, if it declares one.
func (e Endpoint) Validator() (JWTValidator, bool, error) {
	raw, declared := e.ExtraConfig[ValidatorNamespace]
	if !declared {
		return JWTValidator{}, false, nil
	}
	var validator JWTValidator
	if err := json.Unmarshal(raw, &validator); err != nil {
		return JWTValidator{}, true, fmt.Errorf("krakendconfig: decode %s of %s: %w", ValidatorNamespace, e.Route(), err)
	}
	return validator, true, nil
}

// UnmarshalJSON keeps the original bytes so recursive assertions, such as the
// absence of any retry directive, can inspect keys this struct does not model.
func (e *Endpoint) UnmarshalJSON(data []byte) error {
	type endpointFields Endpoint
	var fields endpointFields
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*e = Endpoint(fields)
	e.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// Load reads and decodes the configuration file.
func Load(path string) (Config, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("krakendconfig: read %s: %w", path, err)
	}
	return Parse(contents)
}

// Parse decodes configuration bytes and rejects duplicate routes, which would
// otherwise make the effective policy depend on ordering.
func Parse(contents []byte) (Config, error) {
	var config Config
	if err := json.Unmarshal(contents, &config); err != nil {
		return Config{}, fmt.Errorf("krakendconfig: decode: %w", err)
	}
	config.Raw = append(json.RawMessage(nil), contents...)

	seen := make(map[string]struct{}, len(config.Endpoints))
	for _, endpoint := range config.Endpoints {
		route := endpoint.Route()
		if _, duplicate := seen[route]; duplicate {
			return Config{}, fmt.Errorf("krakendconfig: duplicate route %s", route)
		}
		seen[route] = struct{}{}
	}
	return config, nil
}
