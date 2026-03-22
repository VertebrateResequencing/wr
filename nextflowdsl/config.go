/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

package nextflowdsl

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var skippedTopLevelConfigScopes = map[string]struct{}{
	"conda":        {},
	"dag":          {},
	"manifest":     {},
	"notification": {},
	"report":       {},
	"timeline":     {},
	"tower":        {},
	"trace":        {},
	"wave":         {},
	"weblog":       {},
}

// ProcessDefaults holds default values for process directives.
type ProcessDefaults struct {
	Cpus      int
	Memory    int
	Time      int
	Disk      int
	Container string
	Env       map[string]string
}

// ProcessSelector holds directive overrides for a process selector.
type ProcessSelector struct {
	Kind     string
	Pattern  string
	Settings *ProcessDefaults
	Inner    *ProcessSelector
}

// Profile holds profile-scoped config overrides.
type Profile struct {
	Process *ProcessDefaults
	Selectors []*ProcessSelector
	Params  map[string]any
	Executor map[string]any
}

// Config holds parsed Nextflow configuration.
type Config struct {
	Params          map[string]any
	Profiles        map[string]*Profile
	Process         *ProcessDefaults
	Selectors       []*ProcessSelector
	Executor        map[string]any
	ContainerEngine string
	Env             map[string]string
}

// ParseConfig parses a nextflow.config file.
func ParseConfig(r io.Reader) (*Config, error) {
	return ParseConfigWithParams(r, nil)
}

// ParseConfigFromPath parses a nextflow.config file from disk.
func ParseConfigFromPath(path string) (*Config, error) {
	return ParseConfigFromPathWithParams(path, nil)
}

// ParseConfigWithParams parses a nextflow.config file with external params
// available for evaluating params-backed process expressions.
func ParseConfigWithParams(r io.Reader, externalParams map[string]any) (*Config, error) {
	input, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	tokens, err := lex(string(input))
	if err != nil {
		return nil, err
	}

	params, profileParams, err := newConfigParser(tokens).collectParams()
	if err != nil {
		return nil, err
	}

	return newConfigParser(tokens).parse(params, profileParams, externalParams)
}

// ParseConfigFromPathWithParams parses a nextflow.config file from disk with
// external params available for evaluating params-backed process expressions.
func ParseConfigFromPathWithParams(path string, externalParams map[string]any) (*Config, error) {
	tokens, err := loadConfigTokens(path, map[string]struct{}{})
	if err != nil {
		return nil, err
	}

	params, profileParams, err := newConfigParser(tokens).collectParams()
	if err != nil {
		return nil, err
	}

	return newConfigParser(tokens).parse(params, profileParams, externalParams)
}

type processBlock struct {
	defaults  *ProcessDefaults
	selectors []*ProcessSelector
}

type configParser struct {
	tokens []token
	pos    int
}

func newConfigParser(tokens []token) *configParser {
	return &configParser{tokens: tokens}
}

func (p *configParser) parse(knownParams map[string]any, knownProfileParams map[string]map[string]any, externalParams map[string]any) (*Config, error) {
	cfg := &Config{}

	for {
		p.skipSeparators()
		current := p.current()
		if current.typ == tokenEOF {
			return cfg, nil
		}

		if current.typ != tokenIdent {
			return nil, fmt.Errorf("line %d: unexpected token %q", current.line, current.lit)
		}

		switch current.lit {
		case "apptainer", "docker", "singularity":
			engine, err := p.parseContainerScope(current.lit)
			if err != nil {
				return nil, err
			}
			cfg.ContainerEngine = engine
		case "env":
			env, err := p.parseTopLevelEnvBlock()
			if err != nil {
				return nil, err
			}
			cfg.Env = env
		case "executor":
			executor, err := p.parseExecutorBlock()
			if err != nil {
				return nil, err
			}
			cfg.Executor = MergeParams(cfg.Executor, executor)
		case "params":
			params, err := p.parseParamsBlock()
			if err != nil {
				return nil, err
			}
			cfg.Params = MergeParams(cfg.Params, params)
		case "process":
			block, err := p.parseProcessBlock(MergeParams(knownParams, externalParams), true)
			if err != nil {
				return nil, err
			}
			cfg.Process = block.defaults
			cfg.Selectors = block.selectors
		case "profiles":
			profiles, err := p.parseProfilesBlock(knownParams, knownProfileParams, externalParams)
			if err != nil {
				return nil, err
			}
			cfg.Profiles = profiles
		default:
			skipped, err := p.skipUnknownTopLevelConfigScope()
			if err != nil {
				return nil, err
			}
			if skipped {
				continue
			}
			return nil, fmt.Errorf("line %d: unsupported config section %q", current.line, current.lit)
		}
	}
}

func (p *configParser) collectParams() (map[string]any, map[string]map[string]any, error) {
	params := map[string]any{}
	profileParams := map[string]map[string]any{}

	for {
		p.skipSeparators()
		current := p.current()
		if current.typ == tokenEOF {
			return params, profileParams, nil
		}

		if current.typ != tokenIdent {
			return nil, nil, fmt.Errorf("line %d: unexpected token %q", current.line, current.lit)
		}

		switch current.lit {
		case "apptainer", "docker", "singularity":
			if err := p.skipNamedBlock(current.lit); err != nil {
				return nil, nil, err
			}
		case "env":
			if err := p.skipNamedBlock("env"); err != nil {
				return nil, nil, err
			}
		case "executor":
			if err := p.skipNamedBlock("executor"); err != nil {
				return nil, nil, err
			}
		case "params":
			sectionParams, err := p.parseParamsBlock()
			if err != nil {
				return nil, nil, err
			}
			params = MergeParams(params, sectionParams)
		case "process":
			if err := p.skipNamedBlock("process"); err != nil {
				return nil, nil, err
			}
		case "profiles":
			sectionProfiles, err := p.collectProfileParams()
			if err != nil {
				return nil, nil, err
			}
			for name, sectionParams := range sectionProfiles {
				profileParams[name] = MergeParams(profileParams[name], sectionParams)
			}
		default:
			skipped, err := p.skipUnknownTopLevelConfigScope()
			if err != nil {
				return nil, nil, err
			}
			if skipped {
				continue
			}
			return nil, nil, fmt.Errorf("line %d: unsupported config section %q", current.line, current.lit)
		}
	}
}

func (p *configParser) parseParamsBlock() (map[string]any, error) {
	if _, err := p.expectIdent("params"); err != nil {
		return nil, err
	}

	if _, err := p.expectType(tokenLBrace, "{"); err != nil {
		return nil, err
	}

	params := map[string]any{}
	for {
		p.skipSeparators()
		current := p.current()

		switch current.typ {
		case tokenEOF:
			return nil, p.unclosedBlockError("params")
		case tokenRBrace:
			p.pos++
			return params, nil
		case tokenIdent:
			value, err := p.parseAssignmentValue()
			if err != nil {
				return nil, err
			}
			params[current.lit] = value
		default:
			return nil, fmt.Errorf("line %d: expected parameter name", current.line)
		}
	}
}

func (p *configParser) parseProcessBlock(params map[string]any, allowSelectors bool) (*processBlock, error) {
	if _, err := p.expectIdent("process"); err != nil {
		return nil, err
	}

	if _, err := p.expectType(tokenLBrace, "{"); err != nil {
		return nil, err
	}

	return p.parseProcessSettingsBlock(params, allowSelectors, "process")
}

func (p *configParser) parseProcessSettingsBlock(params map[string]any, allowSelectors bool, blockName string) (*processBlock, error) {
	defaults := &ProcessDefaults{}
	var selectors []*ProcessSelector

	for {
		p.skipSeparators()
		current := p.current()

		switch current.typ {
		case tokenEOF:
			return nil, p.unclosedBlockError(blockName)
		case tokenRBrace:
			p.pos++
			return &processBlock{defaults: defaults, selectors: selectors}, nil
		case tokenIdent:
			if allowSelectors && isProcessSelectorName(current.lit) {
				selector, err := p.parseProcessSelector(params)
				if err != nil {
					return nil, err
				}

				selectors = append(selectors, selector)

				continue
			}

			if err := p.parseProcessAssignment(defaults, params); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("line %d: expected process setting", current.line)
		}
	}
}

func isProcessSelectorName(name string) bool {
	return name == "withLabel" || name == "withName"
}

func (p *configParser) parseProcessSelector(params map[string]any) (*ProcessSelector, error) {
	return p.parseProcessSelectorWithDepth(params, 1)
}

func (p *configParser) parseProcessSelectorWithDepth(params map[string]any, remainingDepth int) (*ProcessSelector, error) {
	name := p.current()
	p.pos++

	if _, err := p.expectType(tokenColon, ":"); err != nil {
		return nil, err
	}

	value, err := p.parseValue(nil, tokenLBrace)
	if err != nil {
		return nil, wrapLineError(name.line, err)
	}

	pattern, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("line %d: %s expects a string literal pattern", name.line, name.lit)
	}

	if _, err := p.expectType(tokenLBrace, "{"); err != nil {
		return nil, err
	}

	defaults := &ProcessDefaults{}
	var inner *ProcessSelector

	for {
		p.skipSeparators()
		current := p.current()

		switch current.typ {
		case tokenEOF:
			return nil, p.unclosedBlockError(name.lit)
		case tokenRBrace:
			p.pos++
			return &ProcessSelector{
				Kind:     name.lit,
				Pattern:  pattern,
				Settings: defaults,
				Inner:    inner,
			}, nil
		case tokenIdent:
			if isProcessSelectorName(current.lit) {
				if remainingDepth == 0 {
					return nil, fmt.Errorf("line %d: nested process selectors may only be one level deep", current.line)
				}

				if inner != nil {
					return nil, fmt.Errorf("line %d: selector %s may contain only one nested selector", current.line, name.lit)
				}

				var err error
				inner, err = p.parseProcessSelectorWithDepth(params, remainingDepth-1)
				if err != nil {
					return nil, err
				}

				continue
			}

			if err := p.parseProcessAssignment(defaults, params); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("line %d: expected process setting", current.line)
		}
	}
}

func (p *configParser) parseProcessAssignment(defaults *ProcessDefaults, params map[string]any) error {
	name := p.current()
	p.pos++

	if name.lit == "env" && p.current().typ == tokenLBrace {
		env, err := p.parseEnvBlock()
		if err != nil {
			return err
		}
		defaults.Env = env
		return nil
	}

	if _, err := p.expectType(tokenAssign, "="); err != nil {
		return err
	}

	value, err := p.parseValue(exprVars(params), tokenSemicolon, tokenNewline, tokenRBrace)
	if err != nil {
		return err
	}

	switch name.lit {
	case "cpus":
		intValue, ok := value.(int)
		if !ok {
			return fmt.Errorf("line %d: cpus expects an integer value", name.line)
		}
		defaults.Cpus = intValue
	case "memory":
		stringValue, ok := value.(string)
		if !ok {
			return fmt.Errorf("line %d: memory expects a string literal", name.line)
		}
		memory, parseErr := parseMemoryValue(stringValue)
		if parseErr != nil {
			return wrapLineError(name.line, parseErr)
		}
		defaults.Memory = memory
	case "time":
		stringValue, ok := value.(string)
		if !ok {
			return fmt.Errorf("line %d: time expects a string literal", name.line)
		}
		timeValue, parseErr := parseTimeValue(stringValue)
		if parseErr != nil {
			return wrapLineError(name.line, parseErr)
		}
		defaults.Time = timeValue
	case "disk":
		stringValue, ok := value.(string)
		if !ok {
			return fmt.Errorf("line %d: disk expects a string literal", name.line)
		}
		diskValue, parseErr := parseDiskValue(stringValue)
		if parseErr != nil {
			return wrapLineError(name.line, parseErr)
		}
		defaults.Disk = diskValue
	case "container":
		stringValue, ok := value.(string)
		if !ok {
			return fmt.Errorf("line %d: container expects a string literal", name.line)
		}
		defaults.Container = stringValue
	default:
		return fmt.Errorf("line %d: unsupported process setting %q", name.line, name.lit)
	}

	return nil
}

func (p *configParser) parseProfilesBlock(baseParams map[string]any, knownProfileParams map[string]map[string]any, externalParams map[string]any) (map[string]*Profile, error) {
	if _, err := p.expectIdent("profiles"); err != nil {
		return nil, err
	}

	if _, err := p.expectType(tokenLBrace, "{"); err != nil {
		return nil, err
	}

	profiles := map[string]*Profile{}
	for {
		p.skipSeparators()
		current := p.current()

		switch current.typ {
		case tokenEOF:
			return nil, p.unclosedBlockError("profiles")
		case tokenRBrace:
			p.pos++
			return profiles, nil
		case tokenIdent:
			profile, err := p.parseProfile(baseParams, knownProfileParams[current.lit], externalParams)
			if err != nil {
				return nil, err
			}
			profiles[current.lit] = profile
		default:
			return nil, fmt.Errorf("line %d: expected profile name", current.line)
		}
	}
}

func (p *configParser) collectProfileParams() (map[string]map[string]any, error) {
	if _, err := p.expectIdent("profiles"); err != nil {
		return nil, err
	}

	if _, err := p.expectType(tokenLBrace, "{"); err != nil {
		return nil, err
	}

	profiles := map[string]map[string]any{}
	for {
		p.skipSeparators()
		current := p.current()

		switch current.typ {
		case tokenEOF:
			return nil, p.unclosedBlockError("profiles")
		case tokenRBrace:
			p.pos++
			return profiles, nil
		case tokenIdent:
			params, err := p.collectProfileParamsBlock()
			if err != nil {
				return nil, err
			}
			profiles[current.lit] = MergeParams(profiles[current.lit], params)
		default:
			return nil, fmt.Errorf("line %d: expected profile name", current.line)
		}
	}
}

func (p *configParser) collectProfileParamsBlock() (map[string]any, error) {
	name, err := p.expectType(tokenIdent, "profile name")
	if err != nil {
		return nil, err
	}

	if _, err = p.expectType(tokenLBrace, "{"); err != nil {
		return nil, err
	}

	params := map[string]any{}
	for {
		p.skipSeparators()
		current := p.current()

		switch current.typ {
		case tokenEOF:
			return nil, p.unclosedNamedBlockError("profile", name.lit)
		case tokenRBrace:
			p.pos++
			return params, nil
		case tokenIdent:
			switch current.lit {
			case "params":
				profileParams, parseErr := p.parseParamsBlock()
				if parseErr != nil {
					return nil, parseErr
				}
				params = MergeParams(params, profileParams)
			case "executor":
				if err := p.skipNamedBlock("executor"); err != nil {
					return nil, err
				}
			case "process":
				if err := p.skipNamedBlock("process"); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("line %d: unsupported profile section %q", current.line, current.lit)
			}
		default:
			return nil, fmt.Errorf("line %d: expected profile section", current.line)
		}
	}
}

func (p *configParser) parseProfile(baseParams map[string]any, knownParams map[string]any, externalParams map[string]any) (*Profile, error) {
	name, err := p.expectType(tokenIdent, "profile name")
	if err != nil {
		return nil, err
	}

	if _, err = p.expectType(tokenLBrace, "{"); err != nil {
		return nil, err
	}

	profile := &Profile{}
	for {
		p.skipSeparators()
		current := p.current()

		switch current.typ {
		case tokenEOF:
			return nil, p.unclosedNamedBlockError("profile", name.lit)
		case tokenRBrace:
			p.pos++
			return profile, nil
		case tokenIdent:
			switch current.lit {
			case "params":
				params, parseErr := p.parseParamsBlock()
				if parseErr != nil {
					return nil, parseErr
				}
				profile.Params = MergeParams(profile.Params, params)
			case "executor":
				executor, parseErr := p.parseExecutorBlock()
				if parseErr != nil {
					return nil, parseErr
				}
				profile.Executor = MergeParams(profile.Executor, executor)
			case "process":
				block, parseErr := p.parseProcessBlock(MergeParams(baseParams, knownParams, externalParams), true)
				if parseErr != nil {
					return nil, parseErr
				}
				profile.Process = block.defaults
				profile.Selectors = block.selectors
			default:
				return nil, fmt.Errorf("line %d: unsupported profile section %q", current.line, current.lit)
			}
		default:
			return nil, fmt.Errorf("line %d: expected profile section", current.line)
		}
	}
}

func (p *configParser) skipNamedBlock(name string) error {
	if _, err := p.expectIdent(name); err != nil {
		return err
	}
	if _, err := p.expectType(tokenLBrace, "{"); err != nil {
		return err
	}

	depth := 1
	for depth > 0 {
		current := p.current()
		if current.typ == tokenEOF {
			return p.unclosedBlockError(name)
		}

		switch current.typ {
		case tokenLBrace:
			depth++
		case tokenRBrace:
			depth--
		}
		p.pos++
	}

	return nil
}

func (p *configParser) skipUnknownTopLevelConfigScope() (bool, error) {
	current := p.current()
	if _, ok := skippedTopLevelConfigScopes[current.lit]; !ok {
		return false, nil
	}

	_, _ = fmt.Fprintf(os.Stderr, "nextflowdsl: skipping unsupported top-level config scope %q at line %d\n", current.lit, current.line)

	if err := p.skipNamedBlock(current.lit); err != nil {
		return false, err
	}

	return true, nil
}

func (p *configParser) parseEnvBlock() (map[string]string, error) {
	if _, err := p.expectType(tokenLBrace, "{"); err != nil {
		return nil, err
	}

	env := map[string]string{}
	for {
		p.skipSeparators()
		current := p.current()

		switch current.typ {
		case tokenEOF:
			return nil, p.unclosedBlockError("env")
		case tokenRBrace:
			p.pos++
			return env, nil
		case tokenIdent:
			value, err := p.parseAssignmentValue()
			if err != nil {
				return nil, err
			}
			stringValue, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("line %d: env value expects a string literal", current.line)
			}
			env[current.lit] = stringValue
		default:
			return nil, fmt.Errorf("line %d: expected environment variable name", current.line)
		}
	}
}

func (p *configParser) parseTopLevelEnvBlock() (map[string]string, error) {
	if _, err := p.expectIdent("env"); err != nil {
		return nil, err
	}

	return p.parseEnvBlock()
}

func (p *configParser) parseExecutorBlock() (map[string]any, error) {
	if _, err := p.expectIdent("executor"); err != nil {
		return nil, err
	}

	if _, err := p.expectType(tokenLBrace, "{"); err != nil {
		return nil, err
	}

	executor := map[string]any{}
	for {
		p.skipSeparators()
		current := p.current()

		switch current.typ {
		case tokenEOF:
			return nil, p.unclosedBlockError("executor")
		case tokenRBrace:
			p.pos++
			return executor, nil
		case tokenIdent:
			value, err := p.parseAssignmentValue()
			if err != nil {
				return nil, err
			}

			switch current.lit {
			case "name", "queue", "clusterOptions":
				stringValue, ok := value.(string)
				if !ok {
					return nil, fmt.Errorf("line %d: executor %s expects a string literal", current.line, current.lit)
				}
				executor[current.lit] = stringValue
			case "queueSize":
				intValue, ok := value.(int)
				if !ok {
					return nil, fmt.Errorf("line %d: executor queueSize expects an integer value", current.line)
				}
				executor[current.lit] = intValue
			default:
				executor[current.lit] = value
			}
		default:
			return nil, fmt.Errorf("line %d: expected executor setting", current.line)
		}
	}
}

func (p *configParser) parseContainerScope(scopeName string) (string, error) {
	if _, err := p.expectIdent(scopeName); err != nil {
		return "", err
	}

	if _, err := p.expectType(tokenLBrace, "{"); err != nil {
		return "", err
	}

	enabled := false
	for {
		p.skipSeparators()
		current := p.current()

		switch current.typ {
		case tokenEOF:
			return "", p.unclosedBlockError(scopeName)
		case tokenRBrace:
			p.pos++
			if enabled {
				return scopeName, nil
			}

			return "", nil
		case tokenIdent:
			value, err := p.parseAssignmentValue()
			if err != nil {
				return "", err
			}

			if current.lit != "enabled" {
				return "", fmt.Errorf("line %d: unsupported %s setting %q", current.line, scopeName, current.lit)
			}

			boolValue, ok := value.(bool)
			if !ok {
				return "", fmt.Errorf("line %d: %s enabled expects a boolean value", current.line, scopeName)
			}

			enabled = boolValue
		default:
			return "", fmt.Errorf("line %d: expected %s setting", current.line, scopeName)
		}
	}
}

func (p *configParser) parseAssignmentValue() (any, error) {
	name := p.current()
	p.pos++

	if _, err := p.expectType(tokenAssign, "="); err != nil {
		return nil, err
	}

	value, err := p.parseValue(nil, tokenSemicolon, tokenNewline, tokenRBrace)
	if err != nil {
		return nil, wrapLineError(name.line, err)
	}

	return value, nil
}

func (p *configParser) parseValue(vars map[string]any, terminators ...tokenType) (any, error) {
	tokens, err := p.readExprTokens(terminators...)
	if err != nil {
		return nil, err
	}

	expr, err := parseExprTokens(tokens)
	if err != nil {
		return nil, wrapLineError(tokens[0].line, err)
	}

	return exprToValue(expr, vars)
}

func exprToValue(expr Expr, vars map[string]any) (any, error) {
	switch value := expr.(type) {
	case IntExpr:
		return value.Value, nil
	case StringExpr:
		return value.Value, nil
	case BoolExpr:
		return value.Value, nil
	case ParamsExpr:
		if vars == nil {
			return nil, fmt.Errorf("unsupported expression %q", "params."+value.Path)
		}

		resolved, err := EvalExpr(value, vars)
		if err != nil {
			return nil, err
		}

		return resolved, nil
	default:
		resolved, err := EvalExpr(expr, vars)
		if err != nil {
			return nil, err
		}

		return resolved, nil
	}
}

func exprVars(params map[string]any) map[string]any {
	if len(params) == 0 {
		return nil
	}

	return map[string]any{"params": params}
}

func (p *configParser) readExprTokens(terminators ...tokenType) ([]token, error) {
	start := p.current()
	tokens := []token{}
	depth := 0

	for {
		current := p.current()
		if current.typ == tokenEOF {
			if len(tokens) == 0 {
				return nil, fmt.Errorf("line %d: expected expression", start.line)
			}
			return tokens, nil
		}

		if depth == 0 && hasTokenType(terminators, current.typ) {
			if len(tokens) == 0 {
				return nil, fmt.Errorf("line %d: expected expression", current.line)
			}
			return tokens, nil
		}

		if current.typ == tokenLParen {
			depth++
		} else if current.typ == tokenRParen && depth > 0 {
			depth--
		}

		tokens = append(tokens, current)
		p.pos++
	}
}

func (p *configParser) skipSeparators() {
	for {
		switch p.current().typ {
		case tokenNewline, tokenSemicolon:
			p.pos++
		default:
			return
		}
	}
}

func (p *configParser) expectType(expected tokenType, description string) (token, error) {
	current := p.current()
	if current.typ != expected {
		return token{}, fmt.Errorf("line %d: expected %s", current.line, description)
	}
	if expected == tokenRBrace {
		p.pos++
		return current, nil
	}
	p.pos++

	return current, nil
}

func (p *configParser) expectIdent(expected string) (token, error) {
	current := p.current()
	if current.typ != tokenIdent || current.lit != expected {
		return token{}, fmt.Errorf("line %d: expected %q", current.line, expected)
	}
	p.pos++

	return current, nil
}

func (p *configParser) current() token {
	if p.pos >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}

	return p.tokens[p.pos]
}

func (p *configParser) previous() token {
	if p.pos == 0 {
		return token{}
	}

	return p.tokens[p.pos-1]
}

func (p *configParser) unclosedBlockError(name string) error {
	line := p.previous().line
	if line == 0 {
		line = p.current().line
	}

	return fmt.Errorf("line %d: expected } to close %s", line, name)
}

func (p *configParser) unclosedNamedBlockError(kind, name string) error {
	line := p.previous().line
	if line == 0 {
		line = p.current().line
	}

	return fmt.Errorf("line %d: expected } to close %s %q", line, kind, name)
}

func loadConfigTokens(path string, visited map[string]struct{}) ([]token, error) {
	resolvedPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	if _, ok := visited[resolvedPath]; ok {
		return nil, fmt.Errorf("circular includeConfig detected for %q", resolvedPath)
	}

	input, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", resolvedPath, err)
	}

	visited[resolvedPath] = struct{}{}
	defer delete(visited, resolvedPath)

	tokens, err := lex(string(input))
	if err != nil {
		return nil, err
	}

	return expandConfigIncludes(tokens, filepath.Dir(resolvedPath), visited)
}

func expandConfigIncludes(tokens []token, baseDir string, visited map[string]struct{}) ([]token, error) {
	expanded := make([]token, 0, len(tokens))
	depth := 0

	for index := 0; index < len(tokens); index++ {
		current := tokens[index]
		if depth == 0 && current.typ == tokenIdent && current.lit == "includeConfig" {
			if index+1 >= len(tokens) {
				return nil, fmt.Errorf("line %d: expected includeConfig path", current.line)
			}

			pathToken := tokens[index+1]
			if pathToken.typ != tokenString {
				return nil, fmt.Errorf("line %d: expected includeConfig path string", pathToken.line)
			}

			includeTokens, err := loadConfigTokens(filepath.Join(baseDir, pathToken.lit), visited)
			if err != nil {
				return nil, err
			}

			expanded = append(expanded, includeTokens[:len(includeTokens)-1]...)
			index++
			continue
		}

		expanded = append(expanded, current)

		switch current.typ {
		case tokenLBrace:
			depth++
		case tokenRBrace:
			if depth > 0 {
				depth--
			}
		}
	}

	return expanded, nil
}
