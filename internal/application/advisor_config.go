package application

import (
	"encoding/json"
	"errors"
	"fmt"
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
}

const ProviderOpenAICompatible = "openai_compatible"

// AdvisorStatus is what the settings UI shows. It never carries the key.
type AdvisorStatus struct {
	Configured  bool            `json:"configured"`
	HasKey      bool            `json:"hasKey"`
	Description string          `json:"description"`
	Settings    AdvisorSettings `json:"settings"`
	// Fault is why a previously saved configuration did not come back at
	// startup. Silently falling back to the rule layer is a working state, but
	// leaving the user to discover on their own that the AI they configured is
	// simply off -- with no reason given -- is not.
	Fault string `json:"fault"`
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

// ConfigureAdvisor validates, persists and installs the advisor. An empty key
// keeps whatever is already stored, so re-saving the endpoint does not require
// the user to paste the credential again.
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
			key = existing
		}
	}
	advisor, err := s.advisorFactory(settings, key)
	if err != nil {
		return AdvisorStatus{}, err
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return AdvisorStatus{}, err
	}
	if err := s.credentials.StoreCredential(advisorConfigAccount, string(encoded)); err != nil {
		return AdvisorStatus{}, err
	}
	if key != "" {
		if err := s.credentials.StoreCredential(advisorKeyAccount, key); err != nil {
			return AdvisorStatus{}, err
		}
	}
	s.SetAdvisor(advisor)
	return AdvisorStatus{Configured: true, HasKey: key != "", Description: advisor.Describe(), Settings: settings}, nil
}

// RestoreAdvisor installs whatever was configured previously. Falling back to
// the rule layer is a working state, so this never fails the startup -- but it
// records why, because "the AI I configured is silently off" is not something a
// user should have to guess at.
func (s *Service) RestoreAdvisor() {
	s.advisorMu.Lock()
	s.advisorFault = ""
	s.advisorMu.Unlock()
	if s.credentials == nil || s.advisorFactory == nil {
		return
	}
	raw, err := s.credentials.LoadCredential(advisorConfigAccount)
	if err != nil {
		if !errors.Is(err, ports.ErrCredentialNotFound) {
			s.setAdvisorFault("读取已保存的配置失败：" + err.Error())
		}
		return
	}
	if strings.TrimSpace(raw) == "" {
		return
	}
	var settings AdvisorSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		s.setAdvisorFault("已保存的配置无法解析：" + err.Error())
		return
	}
	if err := settings.validate(); err != nil {
		s.setAdvisorFault("已保存的配置不合法：" + err.Error())
		return
	}
	key, keyErr := s.credentials.LoadCredential(advisorKeyAccount)
	if keyErr != nil && !errors.Is(keyErr, ports.ErrCredentialNotFound) {
		s.setAdvisorFault("读取已保存的 API key 失败：" + keyErr.Error())
		return
	}
	if strings.TrimSpace(key) == "" {
		s.setAdvisorFault("没有保存的 API key，请重新填写。")
		return
	}
	advisor, err := s.advisorFactory(settings, key)
	if err != nil {
		s.setAdvisorFault("无法按保存的配置建立连接：" + err.Error())
		return
	}
	s.SetAdvisor(advisor)
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
	if raw, err := s.credentials.LoadCredential(advisorConfigAccount); err == nil {
		_ = json.Unmarshal([]byte(raw), &status.Settings)
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
	if s.credentials == nil {
		return nil
	}
	if err := s.credentials.DeleteCredential(advisorKeyAccount); err != nil {
		return err
	}
	return s.credentials.DeleteCredential(advisorConfigAccount)
}

// AdvisorFactory builds an advisor from settings and a key. It is injected by
// the composition root so the application layer never depends on a transport
// implementation.
type AdvisorFactory func(AdvisorSettings, string) (ports.Advisor, error)
