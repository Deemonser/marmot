package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"

	"example.com/marmot/internal/ports"
)

// Two entries rather than one blob so the secret can be deleted without losing
// the endpoint, and so anything that reads the non-secret half cannot reach the
// key by accident.
const (
	advisorConfigAccount = "advisor-config"
	advisorKeyAccount    = "advisor-key"
)

// AdvisorSettings is the non-secret half of the configuration.
type AdvisorSettings struct {
	// Provider is the protocol family, not the vendor: one OpenAI-compatible
	// adapter serves DeepSeek, OpenAI, Kimi, Qwen, OpenRouter and a local
	// vLLM/Ollama alike (ADR-0061 §5).
	Provider string `json:"provider"`
	BaseURL  string `json:"baseUrl"`
	Model    string `json:"model"`
	JSONMode string `json:"jsonMode"`
	// ReasoningEffort is passed through to providers that accept one. Empty
	// omits it, which is what an endpoint that does not know the field needs.
	ReasoningEffort string `json:"reasoningEffort"`
	// Disabled is the switch: the configuration and the key stay on disk, but
	// no advisor is installed and no request leaves the machine. Stored in the
	// negative so a configuration saved before the switch existed -- which
	// unmarshals to false -- keeps doing what it did, which is run.
	Disabled bool `json:"disabled"`
}

const ProviderOpenAICompatible = "openai_compatible"

// AdvisorStatus is what the settings UI shows. It never carries the key.
type AdvisorStatus struct {
	// Configured is whether an advisor is installed right now: the switch is on,
	// the key is there, and the client could be built. This is what "AI 分析"
	// on the button means.
	Configured bool `json:"configured"`
	// Saved is whether a configuration is on disk at all, on or off, working or
	// not. The switch in the settings sheet has nothing to act on without one.
	Saved bool `json:"saved"`
	// Enabled is the switch's position, independent of whether it worked.
	Enabled     bool            `json:"enabled"`
	HasKey      bool            `json:"hasKey"`
	Description string          `json:"description"`
	Settings    AdvisorSettings `json:"settings"`
	// Fault is why a previously saved configuration did not come back at
	// startup. Silently falling back to the rule layer is a working state, but
	// leaving the user to discover on their own that the AI they configured is
	// simply off -- with no reason given -- is not.
	Fault string `json:"fault"`
}

// ReasoningOmit is the explicit choice to send no thinking field at all, for an
// endpoint that would reject one. It has to be a value distinct from the empty
// string, because empty is also what a config saved before this field existed
// unmarshals to -- and those two must never mean the same thing.
//
// They did, and it cost real time. The stored config on the reference machine had
// no reasoning effort at all, so the app sent no thinking block and got the
// provider's own default, which on deepseek-v4-flash is the most expensive one.
// The probe defaults an empty value to "low", so every measurement I took was of
// a setting the app never ran. A default that exists in the measuring tool and
// not in the product is worse than no default: it makes the product's real
// behaviour invisible.
const ReasoningOmit = "omit"

// DefaultReasoningEffort is what an unset effort means. Measured on one identical
// pack, deepseek-v4-flash: thinking disabled 34.4s, "low" 118-191s, and the
// provider default slower still (R-063 §4e).
const DefaultReasoningEffort = "disabled"

// resolved is the settings as they actually take effect, which is what the user
// must be shown. Only the empty legacy value moves.
func (s AdvisorSettings) resolved() AdvisorSettings {
	if strings.TrimSpace(s.ReasoningEffort) == "" {
		s.ReasoningEffort = DefaultReasoningEffort
	}
	return s
}

// forClient additionally turns the explicit omit choice into the empty string the
// adapter reads as "leave the field out". Never persisted in this form, or the
// choice would decay into the legacy value on the next restore.
func (s AdvisorSettings) forClient() AdvisorSettings {
	s = s.resolved()
	if s.ReasoningEffort == ReasoningOmit {
		s.ReasoningEffort = ""
	}
	return s
}

func (s AdvisorSettings) validate() error {
	if s.Provider != ProviderOpenAICompatible {
		return fmt.Errorf("不支持的 provider: %q", s.Provider)
	}
	if strings.TrimSpace(s.BaseURL) == "" {
		return errors.New("endpoint 不能为空")
	}
	if strings.TrimSpace(s.Model) == "" {
		return errors.New("模型名不能为空")
	}
	return nil
}

// errNoKey is the one reason a saved configuration most often fails to come
// back. It used to be possible to save without a key at all: the endpoint was
// written, the key was not, the advisor ran for that one session, and every
// launch after it fell back to the rule layer with the reason two clicks away.
const errNoKey = "没有 API key。远程服务需要一个 key；只有本机地址（localhost）可以留空。"

// keyOptional is whether an endpoint may be used without a credential: a local
// vLLM or Ollama has none to give. Everything else is refused without one.
func keyOptional(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return true
	}
	return false
}

// ConfigureAdvisor validates, persists and installs the advisor. An empty key
// keeps whatever is already stored, so re-saving the endpoint does not require
// the user to paste the credential again. Saving is also switching on: a
// configuration the user just filled in and saved is one they mean to use.
func (s *Service) ConfigureAdvisor(settings AdvisorSettings, apiKey string) (AdvisorStatus, error) {
	if s.credentials == nil || s.advisorFactory == nil {
		return AdvisorStatus{}, errors.New("当前构建不支持配置 AI 分析")
	}
	if err := settings.validate(); err != nil {
		return AdvisorStatus{}, err
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		existing, err := s.credentials.LoadCredential(advisorKeyAccount)
		if err == nil {
			key = strings.TrimSpace(existing)
		}
	}
	if key == "" && !keyOptional(settings.BaseURL) {
		return AdvisorStatus{}, errors.New(errNoKey)
	}
	settings.Disabled = false
	advisor, err := s.advisorFactory(settings.forClient(), key)
	if err != nil {
		return AdvisorStatus{}, err
	}
	if err := s.storeSettings(settings); err != nil {
		return AdvisorStatus{}, err
	}
	if key != "" {
		if err := s.credentials.StoreCredential(advisorKeyAccount, key); err != nil {
			return AdvisorStatus{}, err
		}
	}
	s.setAdvisorFault("")
	s.SetAdvisor(advisor)
	log.Printf("advisor: 已保存并启用 %s", advisor.Describe())
	return s.GetAdvisorStatus(), nil
}

// SetAdvisorEnabled flips the switch on a saved configuration. Off removes the
// advisor and leaves everything on disk; on rebuilds it from what is stored, and
// reports through the status -- not an error -- why that did not work, so the
// sheet shows the reason where the switch is.
func (s *Service) SetAdvisorEnabled(enabled bool) (AdvisorStatus, error) {
	if s.credentials == nil || s.advisorFactory == nil {
		return AdvisorStatus{}, errors.New("当前构建不支持配置 AI 分析")
	}
	settings, saved, err := s.loadSettings()
	if err != nil {
		return AdvisorStatus{}, err
	}
	if !saved {
		return AdvisorStatus{}, errors.New("还没有保存 AI 配置。")
	}
	settings.Disabled = !enabled
	if err := s.storeSettings(settings); err != nil {
		return AdvisorStatus{}, err
	}
	s.setAdvisorFault("")
	if !enabled {
		s.SetAdvisor(nil)
		log.Printf("advisor: 已关闭，仅使用本机规则")
		return s.GetAdvisorStatus(), nil
	}
	if fault := s.installStored(settings); fault != "" {
		s.setAdvisorFault(fault)
		log.Printf("advisor: 开启失败：%s", fault)
	} else {
		log.Printf("advisor: 已开启 %s", s.AdvisorDescription())
	}
	return s.GetAdvisorStatus(), nil
}

// RestoreAdvisor installs whatever was configured previously. Falling back to
// the rule layer is a working state, so this never fails the startup -- but it
// records why, because "the AI I configured is silently off" is not something a
// user should have to guess at. The log carries the outcome either way: the
// last time this went wrong the only evidence was a missing file in a directory
// nobody had reason to open.
func (s *Service) RestoreAdvisor() {
	s.setAdvisorFault("")
	if s.credentials == nil || s.advisorFactory == nil {
		return
	}
	settings, saved, err := s.loadSettings()
	if err != nil {
		s.setAdvisorFault(err.Error())
		log.Printf("advisor: 未恢复：%v", err)
		return
	}
	if !saved {
		log.Printf("advisor: 未配置，仅使用本机规则")
		return
	}
	if settings.Disabled {
		log.Printf("advisor: 已关闭（%s），仅使用本机规则", settings.Model)
		return
	}
	if fault := s.installStored(settings); fault != "" {
		s.setAdvisorFault(fault)
		log.Printf("advisor: 未恢复：%s", fault)
		return
	}
	log.Printf("advisor: 已恢复 %s", s.AdvisorDescription())
}

// loadSettings reads the stored configuration. saved is false when there is
// none, which is the shipping state and not an error; err is a configuration
// that is there but cannot be used, worded for the user.
func (s *Service) loadSettings() (settings AdvisorSettings, saved bool, err error) {
	raw, err := s.credentials.LoadCredential(advisorConfigAccount)
	if err != nil {
		if errors.Is(err, ports.ErrCredentialNotFound) {
			return AdvisorSettings{}, false, nil
		}
		return AdvisorSettings{}, false, errors.New("读取已保存的配置失败：" + err.Error())
	}
	if strings.TrimSpace(raw) == "" {
		return AdvisorSettings{}, false, nil
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return AdvisorSettings{}, true, errors.New("已保存的配置无法解析：" + err.Error())
	}
	if err := settings.validate(); err != nil {
		return AdvisorSettings{}, true, errors.New("已保存的配置不合法：" + err.Error())
	}
	return settings, true, nil
}

func (s *Service) storeSettings(settings AdvisorSettings) error {
	encoded, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return s.credentials.StoreCredential(advisorConfigAccount, string(encoded))
}

// installStored builds the advisor from a stored configuration and its key, and
// returns the reason it could not, or "" once it is installed.
func (s *Service) installStored(settings AdvisorSettings) string {
	key, err := s.credentials.LoadCredential(advisorKeyAccount)
	if err != nil && !errors.Is(err, ports.ErrCredentialNotFound) {
		return "读取已保存的 API key 失败：" + err.Error()
	}
	key = strings.TrimSpace(key)
	if key == "" && !keyOptional(settings.BaseURL) {
		return "没有保存的 API key，请重新填写。"
	}
	advisor, err := s.advisorFactory(settings.forClient(), key)
	if err != nil {
		return "无法按保存的配置建立连接：" + err.Error()
	}
	s.SetAdvisor(advisor)
	return ""
}

func (s *Service) setAdvisorFault(message string) {
	s.advisorMu.Lock()
	defer s.advisorMu.Unlock()
	s.advisorFault = message
}

// GetAdvisorStatus reports what is configured, never the credential itself.
func (s *Service) GetAdvisorStatus() AdvisorStatus {
	status := AdvisorStatus{Description: s.AdvisorDescription()}
	status.Configured = status.Description != ""
	s.advisorMu.RLock()
	status.Fault = s.advisorFault
	s.advisorMu.RUnlock()
	if s.credentials == nil {
		return status
	}
	if raw, err := s.credentials.LoadCredential(advisorConfigAccount); err == nil && strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &status.Settings)
		status.Saved = true
		status.Enabled = !status.Settings.Disabled
		// Report what actually takes effect, not what is on disk. A config saved
		// before this field existed carries nothing, and showing nothing let the
		// settings sheet display one effort while the request used another.
		status.Settings = status.Settings.resolved()
	}
	if key, err := s.credentials.LoadCredential(advisorKeyAccount); err == nil && strings.TrimSpace(key) != "" {
		status.HasKey = true
	}
	return status
}

// ClearAdvisor removes the stored configuration and the key, and puts the
// feature back to the rule layer.
func (s *Service) ClearAdvisor() error {
	s.SetAdvisor(nil)
	s.setAdvisorFault("")
	if s.credentials == nil {
		return nil
	}
	if err := s.credentials.DeleteCredential(advisorKeyAccount); err != nil {
		return err
	}
	if err := s.credentials.DeleteCredential(advisorConfigAccount); err != nil {
		return err
	}
	log.Printf("advisor: 已删除配置和 key，仅使用本机规则")
	return nil
}

// AdvisorFactory builds an advisor from settings and a key. It is injected by
// the composition root so the application layer never depends on a transport
// implementation.
type AdvisorFactory func(AdvisorSettings, string) (ports.Advisor, error)
