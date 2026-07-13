// Package control owns mutable runtime controls shared by HTTP handlers and
// the orchestrator. Configuration loading stays in config; mutations are
// intentionally limited to the existing dashboard settings surface.
package control

import (
	"errors"
	"strings"
	"sync"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

var (
	ErrInvalidRuntimeMode = errors.New("mode must be paper or live")
	ErrInvalidLLMProvider = errors.New("llm_provider must be anthropic or deepseek")
	ErrRuntimeLLMBaseURL  = errors.New("llm_base_url is startup-only; set CYP_LLM_BASE_URL and restart")
)

type State struct {
	mu       sync.RWMutex
	settings config.Settings
}

func New(settings config.Settings) *State { return &State{settings: settings} }

func (s *State) Settings() config.Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

func (s *State) Snapshot() config.SettingsSnapshot {
	return s.Settings().Snapshot()
}

func (s *State) Kill() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings.Kill
}

func (s *State) SetKill(on bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.Kill = on
	return s.settings.Kill
}

// UpdateSettings applies the exact mutable dashboard subset atomically. Empty
// key values never erase an existing secret. The LLM base URL is startup-only
// so an HTTP caller cannot redirect an already-loaded secret to another host.
func (s *State) UpdateSettings(request contracts.SettingsUpdateRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.settings

	if request.Mode != nil {
		mode := strings.ToLower(strings.TrimSpace(*request.Mode))
		if mode != "paper" && mode != "live" {
			return ErrInvalidRuntimeMode
		}
		next.Mode = mode
	}
	if request.LLMProvider != nil {
		provider := strings.TrimSpace(*request.LLMProvider)
		if provider != "anthropic" && provider != "deepseek" {
			return ErrInvalidLLMProvider
		}
		next.LLMProvider = provider
	}
	if request.LLMModel != nil {
		next.LLMModel = strings.TrimSpace(*request.LLMModel)
	}
	if request.LLMModelFast != nil {
		next.LLMModelFast = strings.TrimSpace(*request.LLMModelFast)
	}
	if request.LLMBaseURL != nil {
		if strings.TrimSpace(*request.LLMBaseURL) != s.settings.LLMBaseURL {
			return ErrRuntimeLLMBaseURL
		}
	}
	if request.AnthropicAPIKey != nil {
		if value := strings.TrimSpace(*request.AnthropicAPIKey); value != "" {
			next.AnthropicAPIKey = config.Secret(value)
		}
	}
	if request.DeepSeekAPIKey != nil {
		if value := strings.TrimSpace(*request.DeepSeekAPIKey); value != "" {
			next.DeepSeekAPIKey = config.Secret(value)
		}
	}
	if err := next.Validate(); err != nil {
		return err
	}
	s.settings = next
	return nil
}
