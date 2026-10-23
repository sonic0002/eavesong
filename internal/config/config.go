// Package config holds the installation-wide settings: the AI provider used
// for reply drafts, and the accounts some sources sign in with. They are
// edited on the web settings page and stored in the database, with secrets
// encrypted. Environment variables give the defaults for anything not saved.
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/sonic0002/eavesong/internal/ai"
	"github.com/sonic0002/eavesong/internal/secret"
	"github.com/sonic0002/eavesong/internal/store"
)

type Settings struct {
	AIProvider string // "none", "anthropic", "deepseek", "gemini" or "openai"
	AIModel    string
	AIBaseURL  string // for "openai": any OpenAI-compatible API, e.g. Ollama
	AIKey      string // secret

	XDailyCap                                                                   string // most paid X posts read per UTC day
	SMTPHost, SMTPPort, SMTPUser, SMTPPassword, SMTPFrom, SMTPTo                string
	SlackWebhook, DiscordWebhook, GenericWebhook                                string
	NotifyAttention, NotifyCompetitor, NotifySitemap, NotifyDaily, NotifyWeekly string
}

// Field names in the settings table, and the environment variable that gives
// each one's default.
var fields = []struct {
	name, env string
	secret    bool
	get       func(*Settings) *string
}{
	{"ai_provider", "EAVESONG_AI_PROVIDER", false, func(s *Settings) *string { return &s.AIProvider }},
	{"ai_model", "EAVESONG_AI_MODEL", false, func(s *Settings) *string { return &s.AIModel }},
	{"ai_base_url", "EAVESONG_AI_BASE_URL", false, func(s *Settings) *string { return &s.AIBaseURL }},
	{"ai_key", "", true, func(s *Settings) *string { return &s.AIKey }},
	{"x_daily_cap", "EAVESONG_X_DAILY_CAP", false, func(s *Settings) *string { return &s.XDailyCap }},
	{"smtp_host", "", false, func(s *Settings) *string { return &s.SMTPHost }},
	{"smtp_port", "", false, func(s *Settings) *string { return &s.SMTPPort }},
	{"smtp_user", "", false, func(s *Settings) *string { return &s.SMTPUser }},
	{"smtp_password", "", true, func(s *Settings) *string { return &s.SMTPPassword }},
	{"smtp_from", "", false, func(s *Settings) *string { return &s.SMTPFrom }},
	{"smtp_to", "", false, func(s *Settings) *string { return &s.SMTPTo }},
	{"slack_webhook", "", true, func(s *Settings) *string { return &s.SlackWebhook }},
	{"discord_webhook", "", true, func(s *Settings) *string { return &s.DiscordWebhook }},
	{"generic_webhook", "", true, func(s *Settings) *string { return &s.GenericWebhook }},
	{"notify_attention", "", false, func(s *Settings) *string { return &s.NotifyAttention }},
	{"notify_competitor", "", false, func(s *Settings) *string { return &s.NotifyCompetitor }},
	{"notify_sitemap", "", false, func(s *Settings) *string { return &s.NotifySitemap }},
	{"notify_daily", "", false, func(s *Settings) *string { return &s.NotifyDaily }},
	{"notify_weekly", "", false, func(s *Settings) *string { return &s.NotifyWeekly }},
}

// Manager loads and saves settings.
type Manager struct {
	Store *store.Store
	Box   *secret.Box
	// Verify checks new account credentials with the platform before they
	// are saved; nil skips the check (used in tests).
	Verify Verifier
}

// Load returns the saved settings, falling back to environment variables.
func (m Manager) Load(ctx context.Context) (Settings, error) {
	saved, err := m.Store.Settings(ctx)
	if err != nil {
		return Settings{}, err
	}
	var s Settings
	for _, f := range fields {
		value := saved[f.name]
		if value != "" && f.secret {
			if value, err = m.Box.Open(value); err != nil {
				return Settings{}, fmt.Errorf("setting %s: %w", f.name, err)
			}
		}
		if value == "" && f.env != "" {
			value = Getenv(f.env)
		}
		*f.get(&s) = value
	}
	if s.AIKey == "" { // the provider's usual variable
		switch s.AIProvider {
		case "anthropic":
			s.AIKey = os.Getenv("ANTHROPIC_API_KEY")
		case "openai":
			s.AIKey = os.Getenv("OPENAI_API_KEY")
		case "deepseek":
			s.AIKey = os.Getenv("DEEPSEEK_API_KEY")
		case "gemini":
			s.AIKey = os.Getenv("GEMINI_API_KEY")
		}
	}
	if s.AIProvider == "" {
		s.AIProvider = "none"
	}
	if s.XDailyCap == "" {
		s.XDailyCap = "100"
	}
	return s, nil
}

// Save stores settings, encrypting the secret ones.
func (m Manager) Save(ctx context.Context, s Settings) error {
	values := map[string]string{}
	for _, f := range fields {
		value := strings.TrimSpace(*f.get(&s))
		if value != "" && f.secret {
			sealed, err := m.Box.Seal(value)
			if err != nil {
				return err
			}
			value = sealed
		}
		values[f.name] = value
	}
	return m.Store.SaveSettings(ctx, values)
}

// Validate checks that the AI settings are complete.
func (s Settings) Validate() error {
	switch s.AIProvider {
	case "none", "":
		return nil
	case "anthropic":
		if s.AIKey == "" {
			return errors.New("Anthropic needs an API key")
		}
	case "deepseek":
		if s.AIKey == "" {
			return errors.New("DeepSeek needs an API key")
		}
	case "gemini":
		if s.AIKey == "" {
			return errors.New("Gemini needs an API key")
		}
	case "openai":
		if s.AIModel == "" {
			return errors.New("an OpenAI-compatible provider needs a model name")
		}
		if s.AIBaseURL == "" {
			return errors.New("an OpenAI-compatible provider needs an API address")
		}
	default:
		return fmt.Errorf("unknown provider %q", s.AIProvider)
	}
	return nil
}

// Default models when none is set.
const (
	DefaultModel         = "claude-sonnet-5-5"
	DefaultDeepSeekModel = "deepseek-flash"
	DeepSeekBaseURL      = "https://api.deepseek.com" // OpenAI-compatible
	DefaultGeminiModel   = "gemini-3.8-flash"
	GeminiBaseURL        = "https://generativelanguage.googleapis.com/v1beta/openai"
)

// Drafter builds the reply drafter, or nil when drafts are off, with a short
// description for the interface.
func (s Settings) Drafter() (ai.Drafter, string) {
	switch s.AIProvider {
	case "anthropic":
		if s.AIKey == "" {
			return nil, ""
		}
		model := s.AIModel
		if model == "" {
			model = DefaultModel
		}
		return ai.Anthropic{APIKey: s.AIKey, Model: model}, "Anthropic, " + model
	case "deepseek":
		if s.AIKey == "" {
			return nil, ""
		}
		model := s.AIModel
		if model == "" {
			model = DefaultDeepSeekModel
		}
		return ai.OpenAICompatible{APIKey: s.AIKey, Model: model, BaseURL: DeepSeekBaseURL}, "DeepSeek, " + model
	case "gemini":
		if s.AIKey == "" {
			return nil, ""
		}
		model := s.AIModel
		if model == "" {
			model = DefaultGeminiModel
		}
		return ai.OpenAICompatible{APIKey: s.AIKey, Model: model, BaseURL: GeminiBaseURL}, "Gemini, " + model
	case "openai":
		if s.AIModel == "" || s.AIBaseURL == "" {
			return nil, ""
		}
		return ai.OpenAICompatible{APIKey: s.AIKey, Model: s.AIModel, BaseURL: s.AIBaseURL}, s.AIModel + " at " + s.AIBaseURL
	}
	return nil, ""
}

// Hint shows the end of a secret, so people can tell which key is saved
// without the key being shown.
func Hint(secret string) string {
	if secret == "" {
		return ""
	}
	if len(secret) <= 8 {
		return "saved"
	}
	return "saved, ends in " + secret[len(secret)-4:]
}

// XCap is the daily limit on paid X reads.
func (s Settings) XCap() int {
	n, err := strconv.Atoi(strings.TrimSpace(s.XDailyCap))
	if err != nil || n < 0 {
		return 100
	}
	return n
}

// Getenv reads a configured environment variable, trimming surrounding space.
func Getenv(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}
