package application

import (
	"strings"
	"testing"

	"example.com/marmot/internal/ports"
)

// memoryCredentials is the store as the tests see it: a map, so a test can say
// exactly what is and is not on disk.
type memoryCredentials map[string]string

func (m memoryCredentials) StoreCredential(account, secret string) error {
	m[account] = secret
	return nil
}
func (m memoryCredentials) LoadCredential(account string) (string, error) {
	secret, ok := m[account]
	if !ok {
		return "", ports.ErrCredentialNotFound
	}
	return secret, nil
}
func (m memoryCredentials) DeleteCredential(account string) error { delete(m, account); return nil }

func newSwitchService(creds memoryCredentials) *Service {
	return NewService(Dependencies{
		Credentials: creds,
		AdvisorFactory: func(AdvisorSettings, string) (ports.Advisor, error) {
			return &fakeAdvisor{}, nil
		},
	})
}

var remote = AdvisorSettings{Provider: ProviderOpenAICompatible, BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-flash"}

// The bug that started this: the endpoint was saved, the key was not, and every
// launch after the first restored nothing. A remote endpoint without a key is
// now refused at the save, where the user can still do something about it.
func TestSaveWithoutKeyIsRefusedForRemoteEndpoint(t *testing.T) {
	creds := memoryCredentials{}
	s := newSwitchService(creds)
	if _, err := s.ConfigureAdvisor(remote, ""); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("saved a remote endpoint with no key: err=%v", err)
	}
	if _, ok := creds[advisorConfigAccount]; ok {
		t.Fatal("a refused save still wrote the configuration")
	}
	if s.GetAdvisorStatus().Configured {
		t.Fatal("a refused save still installed an advisor")
	}
}

// A local vLLM or Ollama has no key to give, and must stay reachable.
func TestSaveWithoutKeyIsAllowedForLocalhost(t *testing.T) {
	for _, base := range []string{"http://localhost:11434/v1", "http://127.0.0.1:8000/v1", "http://[::1]:8000"} {
		s := newSwitchService(memoryCredentials{})
		local := remote
		local.BaseURL = base
		status, err := s.ConfigureAdvisor(local, "")
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		if !status.Configured || !status.Enabled || !status.Saved {
			t.Fatalf("%s: status after save = %+v", base, status)
		}
	}
}

// Re-saving the endpoint keeps the stored key, so editing the model does not
// require pasting the credential again -- and does not trip the new check.
func TestResaveKeepsStoredKey(t *testing.T) {
	creds := memoryCredentials{}
	s := newSwitchService(creds)
	if _, err := s.ConfigureAdvisor(remote, "sk-1"); err != nil {
		t.Fatal(err)
	}
	changed := remote
	changed.Model = "other"
	status, err := s.ConfigureAdvisor(changed, "")
	if err != nil {
		t.Fatal(err)
	}
	if !status.HasKey || creds[advisorKeyAccount] != "sk-1" {
		t.Fatalf("the stored key did not survive a re-save: %+v", status)
	}
}

// The switch: off keeps everything on disk and installs nothing, and restores
// quietly -- being off is not a fault. On rebuilds the advisor from what is
// stored.
func TestSwitchOffRestoresQuietlyAndOnComesBack(t *testing.T) {
	creds := memoryCredentials{}
	s := newSwitchService(creds)
	if _, err := s.ConfigureAdvisor(remote, "sk-1"); err != nil {
		t.Fatal(err)
	}
	status, err := s.SetAdvisorEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if status.Configured || status.Enabled || !status.Saved || !status.HasKey {
		t.Fatalf("after switching off: %+v", status)
	}
	if s.currentAdvisor() != nil {
		t.Fatal("the advisor is still installed with the switch off")
	}

	restarted := newSwitchService(creds)
	restarted.RestoreAdvisor()
	status = restarted.GetAdvisorStatus()
	if status.Configured || status.Fault != "" || !status.Saved || status.Enabled {
		t.Fatalf("a switched-off configuration restored as %+v", status)
	}

	status, err = restarted.SetAdvisorEnabled(true)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Configured || !status.Enabled || status.Fault != "" {
		t.Fatalf("after switching on: %+v", status)
	}
}

// A configuration saved before the switch existed carries no field for it and
// must keep running: the negative storage is what makes zero mean "on".
func TestLegacyConfigWithoutSwitchStaysOn(t *testing.T) {
	creds := memoryCredentials{
		advisorConfigAccount: `{"provider":"openai_compatible","baseUrl":"https://x","model":"m"}`,
		advisorKeyAccount:    "sk-1",
	}
	s := newSwitchService(creds)
	s.RestoreAdvisor()
	status := s.GetAdvisorStatus()
	if !status.Configured || !status.Enabled || status.Fault != "" {
		t.Fatalf("legacy config restored as %+v", status)
	}
}

// The state found on the reference machine: configuration on disk, key gone.
// The switch reads as on, nothing is installed, and the reason is in the status.
func TestMissingKeyIsAFaultNotASilence(t *testing.T) {
	creds := memoryCredentials{advisorConfigAccount: `{"provider":"openai_compatible","baseUrl":"https://x","model":"m"}`}
	s := newSwitchService(creds)
	s.RestoreAdvisor()
	status := s.GetAdvisorStatus()
	if status.Configured || !status.Enabled || !status.Saved || !strings.Contains(status.Fault, "API key") {
		t.Fatalf("missing key restored as %+v", status)
	}
	// Switching it on again cannot help, and says so in the same place.
	status, err := s.SetAdvisorEnabled(true)
	if err != nil {
		t.Fatal(err)
	}
	if status.Configured || !strings.Contains(status.Fault, "API key") {
		t.Fatalf("switching on with no key: %+v", status)
	}
}

// Nothing saved: the switch has nothing to act on.
func TestSwitchWithoutSavedConfigIsAnError(t *testing.T) {
	s := newSwitchService(memoryCredentials{})
	if _, err := s.SetAdvisorEnabled(true); err == nil {
		t.Fatal("switching on with nothing saved succeeded")
	}
}
