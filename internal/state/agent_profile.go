package state

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// agentProfileProviders lists the provider IDs an agent profile may configure.
// It mirrors the provider registry, which cannot be imported from here; a test
// in the provider package keeps the two in sync.
var agentProfileProviders = []string{"claude", "codex", "gemini", "opencode"}

// AgentProfileProviderIDs returns the provider IDs an agent profile may
// configure, sorted.
func AgentProfileProviderIDs() []string {
	return append([]string(nil), agentProfileProviders...)
}

// AgentInvocation customizes how one provider's CLI is launched under an agent
// profile. Every field is optional.
type AgentInvocation struct {
	// Command replaces the provider binary. A bare name is resolved via PATH.
	Command string `json:"command,omitempty"`
	// Args are inserted after Vigilante's own flags and before the prompt.
	Args []string `json:"args,omitempty"`
	// Env is applied on top of the daemon environment for the agent process.
	Env map[string]string `json:"env,omitempty"`
}

// IsZero reports whether the invocation changes nothing.
func (i AgentInvocation) IsZero() bool {
	return i.Command == "" && len(i.Args) == 0 && len(i.Env) == 0
}

// AgentProfile is a named set of per-provider invocation settings from
// config.json. A profile that fails to decode keeps its raw JSON and the
// decode error instead of failing the whole config, so one malformed profile
// cannot take down targets that do not use it.
type AgentProfile struct {
	Providers map[string]AgentInvocation
	// Match is an optional RE2 expression matched against the repository slug.
	Match    string
	matchSet bool

	raw       json.RawMessage
	decodeErr error
}

// UnmarshalJSON decodes a profile strictly, recording rather than returning a
// decode failure; Validate reports it.
func (p *AgentProfile) UnmarshalJSON(data []byte) error {
	*p = AgentProfile{}
	p.raw = append(json.RawMessage(nil), data...)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		p.decodeErr = err
		return nil
	}
	if raw, ok := fields["match"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			p.decodeErr = fmt.Errorf("match must be a string")
		} else if err := json.Unmarshal(raw, &p.Match); err != nil {
			p.decodeErr = fmt.Errorf("match must be a string")
		}
		p.matchSet = p.decodeErr == nil
		delete(fields, "match")
	}
	p.Providers = make(map[string]AgentInvocation)
	for id, raw := range fields {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var invocation AgentInvocation
		if err := decoder.Decode(&invocation); err != nil {
			if p.decodeErr == nil {
				p.decodeErr = fmt.Errorf("provider %q: %w", id, err)
			}
			continue
		}
		p.Providers[id] = invocation
	}
	return nil
}

// MarshalJSON writes the profile back as it was read when it failed to decode,
// so saving the service config never discards an operator's broken entry.
func (p AgentProfile) MarshalJSON() ([]byte, error) {
	if p.decodeErr != nil && len(p.raw) > 0 {
		return p.raw, nil
	}
	fields := make(map[string]any, len(p.Providers)+1)
	for id, invocation := range p.Providers {
		fields[id] = invocation
	}
	if p.matchSet || p.Match != "" {
		fields["match"] = p.Match
	}
	return json.Marshal(fields)
}

// Validate reports the first problem that makes the profile unusable.
func (p AgentProfile) Validate() error {
	if p.matchSet || p.Match != "" {
		if _, err := regexp.Compile(p.Match); err != nil {
			return fmt.Errorf("invalid match regex: %w", err)
		}
	}
	if p.decodeErr != nil {
		return fmt.Errorf("malformed profile: %w", p.decodeErr)
	}
	ids := make([]string, 0, len(p.Providers))
	for id := range p.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !isAgentProfileProvider(id) {
			return fmt.Errorf("unknown provider %q (supported: %s)", id, strings.Join(agentProfileProviders, ", "))
		}
		invocation := p.Providers[id]
		if invocation.Command != "" && strings.TrimSpace(invocation.Command) != invocation.Command {
			return fmt.Errorf("provider %q: command must not have leading or trailing whitespace", id)
		}
		keys := make([]string, 0, len(invocation.Env))
		for key := range invocation.Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if key == "" || strings.ContainsAny(key, "=\x00") || strings.TrimSpace(key) != key {
				return fmt.Errorf("provider %q: invalid env variable name %q", id, key)
			}
			if strings.ContainsRune(invocation.Env[key], 0) {
				return fmt.Errorf("provider %q: env variable %s contains a NUL byte", id, key)
			}
		}
	}
	return nil
}

func isAgentProfileProvider(id string) bool {
	for _, known := range agentProfileProviders {
		if id == known {
			return true
		}
	}
	return false
}

// ValidAgentProfileName reports whether name can identify an agent profile.
func ValidAgentProfileName(name string) bool {
	return name != "" && strings.TrimSpace(name) == name
}

// AgentProfile returns the named profile. It fails with an error naming the
// profile when it is not defined or is invalid.
func (c ServiceConfig) AgentProfile(name string) (AgentProfile, error) {
	profile, ok := c.AgentProfiles[name]
	if !ok || !ValidAgentProfileName(name) {
		return AgentProfile{}, fmt.Errorf("agent profile %q is not defined in config.json (agent_profiles)", name)
	}
	if err := profile.Validate(); err != nil {
		return AgentProfile{}, fmt.Errorf("agent profile %q in config.json is invalid: %w", name, err)
	}
	return profile, nil
}

// AgentInvocation returns the named profile's settings for providerID. A
// profile that does not mention the provider yields the zero value, which
// leaves that provider's invocation unchanged.
func (c ServiceConfig) AgentInvocation(name string, providerID string) (AgentInvocation, error) {
	profile, err := c.AgentProfile(name)
	if err != nil {
		return AgentInvocation{}, err
	}
	return profile.Providers[providerID], nil
}

// AgentProfileErrors returns one error per unusable agent profile, ordered by
// profile name. An empty result means every configured profile is usable.
func (c ServiceConfig) AgentProfileErrors() []error {
	names := make([]string, 0, len(c.AgentProfiles))
	for name := range c.AgentProfiles {
		names = append(names, name)
	}
	sort.Strings(names)
	var errs []error
	for _, name := range names {
		if !ValidAgentProfileName(name) {
			errs = append(errs, fmt.Errorf("agent profile name %q in config.json is invalid: names must be non-empty without surrounding whitespace", name))
			continue
		}
		if err := c.AgentProfiles[name].Validate(); err != nil {
			errs = append(errs, fmt.Errorf("agent profile %q in config.json is invalid: %w", name, err))
		}
	}
	return errs
}

// EffectiveAgentProfile selects an explicit profile or the alphabetically first
// matching profile. Invalid regexes are skipped; other invalid matches block.
func (c ServiceConfig) EffectiveAgentProfile(target WatchTarget) (name string, auto bool, err error) {
	if target.AgentProfile != "" {
		_, err = c.AgentProfile(target.AgentProfile)
		return target.AgentProfile, false, err
	}
	names := make([]string, 0, len(c.AgentProfiles))
	for name := range c.AgentProfiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		profile := c.AgentProfiles[name]
		if !profile.matchSet && profile.Match == "" {
			continue
		}
		re, compileErr := regexp.Compile(profile.Match)
		if compileErr != nil || !re.MatchString(target.Repo) {
			continue
		}
		_, err = c.AgentProfile(name)
		return name, true, err
	}
	return "", false, nil
}
