package application

import "testing"

// A config written before the reasoning field existed unmarshals to an empty
// string, and empty used to mean "send no thinking block", which handed the
// request to the provider's own default -- the slowest and most expensive one.
// The probe defaulted empty to "low" and so measured a setting the app never ran.
func TestLegacyConfigWithNoEffortDoesNotFallToTheProviderDefault(t *testing.T) {
	legacy := AdvisorSettings{Provider: ProviderOpenAICompatible, BaseURL: "https://x", Model: "m"}
	if got := legacy.forClient().ReasoningEffort; got != DefaultReasoningEffort {
		t.Fatalf("an unset effort reaches the client as %q, so the provider chooses", got)
	}
	// And the sheet must show what will actually happen.
	if got := legacy.resolved().ReasoningEffort; got != DefaultReasoningEffort {
		t.Fatalf("the settings sheet would show %q while the request uses another", got)
	}
}

// The escape hatch for an endpoint that rejects a thinking block must survive a
// restart. Storing its client form would turn it back into the legacy value, and
// the next restore would silently switch it to the default.
func TestOmitIsDistinctFromUnsetAndSurvivesPersistence(t *testing.T) {
	omit := AdvisorSettings{Provider: ProviderOpenAICompatible, BaseURL: "https://x", Model: "m", ReasoningEffort: ReasoningOmit}
	if got := omit.forClient().ReasoningEffort; got != "" {
		t.Fatalf("omit reached the client as %q; the field must be left out", got)
	}
	if got := omit.resolved().ReasoningEffort; got != ReasoningOmit {
		t.Fatalf("omit is stored and displayed as %q, so it would decay on restart", got)
	}
}
