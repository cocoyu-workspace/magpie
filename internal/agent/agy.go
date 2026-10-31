package agent

// Antigravity's CLI, agy, keeps its settings in
// ~/.gemini/antigravity-cli/settings.json. With "modelProvider": "gemini" it
// asks a Gemini API endpoint rather than Google's own backend, and lists the
// models of customModelsConfig.customModels, each {apiProvider, modelName},
// the one picked being "model". magpie's gateway speaks the Gemini API, so
// magpie adds one custom model per catalog model, keyed
// "magpie/<provider>/<model>", and sets modelProvider and model; the user's
// own are stashed and put back when magpie steps out. The endpoint and key
// agy takes only from its environment (GOOGLE_GEMINI_BASE_URL and
// GEMINI_API_KEY; no .env file), so magpie can't set them: the agent's row
// gives the command that starts agy with them (see Agent.Launch). That
// names the model with --model too: agy 1.2 asks for a custom model picked
// in settings.json or its /model picker with no model name ("model is
// empty"); only one given on the command line resolves.

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

const agyModels = "customModelsConfig.customModels"

// agyStandIn is the model Antigravity CLI is set to use when routed through
// magpie, for a hardcoded helper model it asks for by a bare name magpie
// doesn't serve (such as gemini-3.1-flash-lite-preview for worktree/title
// naming). Fast/ultrafast suffixes are stripped so background helper turns use
// the standard tier.
func trimAgyVariant(v string) (string, string) {
	orig := v
	for _, suf := range []string{"-max", "-xhigh", "-high", "-medium", "-low"} {
		if base, cut := strings.CutSuffix(v, suf); cut {
			v = base
			break
		}
	}
	for _, suf := range []string{"-ultrafast", "-fast"} {
		if base, cut := strings.CutSuffix(v, suf); cut {
			v = base
			break
		}
	}
	return v, strings.TrimPrefix(orig, v)
}

func agyStandIn(path string) string {
	if p := jsonGet(path, "modelProvider")(); p != "gemini" {
		return ""
	}
	v := jsonGet(path, "model")()
	ref, ok := strings.CutPrefix(v, magpieID+"/")
	if !ok {
		return ""
	}
	base, _ := trimAgyVariant(ref)
	return base
}

// agyModelEntry builds the .exa.codeium_common_pb.ModelInfo JSON entry for one
// catalog model in customModelsConfig.customModels. When catalog metadata
// (context window, output limit, capabilities) is known, it populates
// maxTokens, maxOutputTokens, supportsContext, and modelFeatures so agy's
// context tracker and CompactionConfig know the true window and features.
func agyModelEntry(modelName string, m catalog.Model) map[string]any {
	entry := map[string]any{
		"apiProvider": "API_PROVIDER_GOOGLE_GEMINI",
		"modelName":   modelName,
	}
	if m.Context == 0 && m.Output == 0 && !m.Reasoning && !m.Images {
		return entry
	}
	if m.Context > 0 {
		entry["maxTokens"] = m.Context
	}
	if out := maxTokens(m); out > 0 {
		entry["maxOutputTokens"] = out
	}
	entry["supportsContext"] = true
	entry["modelFeatures"] = map[string]any{
		"supportsImages":            m.Images,
		"supportsThinking":          m.Reasoning,
		"supportsToolCalls":         true,
		"supportsCumulativeContext": true,
		"supportsContextTokens":     true,
	}
	return entry
}

// AgyLaunch is the command that starts agy on magpie's gateway and one of
// its custom models, in the shell of this system. Its key names agy: its
// requests go out as Google's Gemini SDK's (User-Agent google-genai-sdk/…),
// with nothing of agy's own, so the gateway knows them by the key
// (x-goog-api-key) alone. A command copied before, with the plain token,
// still reaches magpie.
func AgyLaunch(model string) string {
	key := agentKey("agy")
	if runtime.GOOS == "windows" {
		return `$env:GEMINI_API_KEY="` + key + `"; $env:GOOGLE_GEMINI_BASE_URL="` + gateway.URL() + `"; agy --model '` + strings.ReplaceAll(model, "'", "''") + `'`
	}
	return "GEMINI_API_KEY=" + key + " GOOGLE_GEMINI_BASE_URL=" + gateway.URL() + " agy --model '" + strings.ReplaceAll(model, "'", `'\''`) + "'"
}

// agyCustom reads customModels as it is, raw JSON by key.
func agyCustom(path string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	raw, err := edit.Read(path)
	if err != nil || len(raw) == 0 {
		return out
	}
	gjson.GetBytes(jsonc.ToJSONInPlace(raw), agyModels).ForEach(func(k, v gjson.Result) bool {
		out[k.String()] = json.RawMessage(v.Raw)
		return true
	})
	return out
}

func agy(home string) *Agent {
	dir := filepath.Join(home, ".gemini", "antigravity-cli")
	path := filepath.Join(dir, "settings.json")
	get := jsonGet(path, "model")
	provider := jsonGet(path, "modelProvider")
	stashModel, stashProvider := "agy:"+path+":model", "agy:"+path+":modelProvider"
	// setModels writes customModels with the user's own and, when with,
	// magpie's; none left takes the key out
	setModels := func(with bool) error {
		ms := map[string]any{}
		for k, v := range agyCustom(path) {
			if !strings.HasPrefix(k, magpieID+"/") {
				ms[k] = v
			}
		}
		if with {
			for _, m := range magpieModels("agy") {
				ms[magpieID+"/"+m.ID] = agyModelEntry(m.ID, m)
				if m.Reasoning || len(m.Efforts) > 0 {
					ms[magpieID+"/"+m.ID+"-high"] = agyModelEntry(m.ID+"-high", m)
					ms[magpieID+"/"+m.ID+"-max"] = agyModelEntry(m.ID+"-max", m)
				}
				if m.CodexFast {
					ms[magpieID+"/"+m.ID+"-fast"] = agyModelEntry(m.ID+"-fast", m)
					ms[magpieID+"/"+m.ID+"-fast-high"] = agyModelEntry(m.ID+"-fast-high", m)
					ms[magpieID+"/"+m.ID+"-fast-max"] = agyModelEntry(m.ID+"-fast-max", m)
					ms[magpieID+"/"+m.ID+"-ultrafast"] = agyModelEntry(m.ID+"-ultrafast", m)
					ms[magpieID+"/"+m.ID+"-ultrafast-high"] = agyModelEntry(m.ID+"-ultrafast-high", m)
					ms[magpieID+"/"+m.ID+"-ultrafast-max"] = agyModelEntry(m.ID+"-ultrafast-max", m)
				}
			}
		}
		if len(ms) > 0 {
			return edit.SetJSON(path, edit.KV{Path: agyModels, Value: ms})
		}
		if err := edit.DelJSON(path, agyModels); err != nil {
			return err
		}
		if v, ok := edit.GetJSON(path, "customModelsConfig"); ok && strings.TrimSpace(v) == "{}" {
			return edit.DelJSON(path, "customModelsConfig")
		}
		return nil
	}
	// put sets key to v, or takes it out for ""
	put := func(key, v string) error {
		if v == "" {
			return edit.DelJSON(path, key)
		}
		return edit.SetJSON(path, edit.KV{Path: key, Value: v})
	}
	return &Agent{
		ID: "agy", Name: "Antigravity CLI", Icon: "antigravity-color", Aliases: []string{"antigravity-cli"}, Spelled: prefixed, NoNames: true,
		Bin: "agy", Dir: dir, Path: path,
		SplitSuffix: func(v string) (string, string, bool) {
			base, suf := trimAgyVariant(v)
			return base, suf, true
		},
		Launch: func() string {
			if v := get(); usesMagpie(v) {
				return AgyLaunch(v)
			}
			return ""
		},
		Sync: func() error {
			if !usesMagpie(get()) {
				return nil
			}
			return setModels(true)
		},
		Notice: func() string {
			v := get()
			if !usesMagpie(v) {
				return ""
			}
			return noticeAgy.say("command", AgyLaunch(v))
		},
		Check: func() string {
			v := get()
			if !usesMagpie(v) {
				return ""
			}
			if _, ok := agyCustom(path)[v]; !ok {
				return "Antigravity CLI's custom model " + v + " (settings.json) is gone, so it no longer reaches magpie"
			}
			if p := provider(); p != "gemini" {
				return "Antigravity CLI's modelProvider (settings.json) is " + orDefault(p) + ", not gemini, so it asks Google directly"
			}
			return ""
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: get,
			Set: func(v string) error {
				cur := get()
				base, _ := trimAgyVariant(v)
				if ref, ok := strings.CutPrefix(base, magpieID+"/"); ok && isMagpie(ref) {
					if !usesMagpie(cur) {
						stash(map[string]string{stashModel: cur, stashProvider: provider()})
					}
					if err := setModels(true); err != nil {
						return err
					}
					return edit.SetJSON(path, edit.KV{Path: "modelProvider", Value: "gemini"}, edit.KV{Path: "model", Value: v})
				}
				if usesMagpie(cur) {
					// back to what the user had
					if v == "" {
						v = unstash(stashModel)
					} else {
						forget(stashModel)
					}
					if err := put("modelProvider", unstash(stashProvider)); err != nil {
						return err
					}
				}
				if err := put("model", v); err != nil {
					return err
				}
				return setModels(false)
			},
			Options: func(cur map[string]string) []Option {
				custom := agyCustom(path)
				// its own say they go to their provider, not through magpie,
				// as every agent's own do (EZN7L2C3, #834)
				var own []Option
				for k := range custom {
					if !strings.HasPrefix(k, magpieID+"/") {
						to := "Google"
						if p := gjson.GetBytes(custom[k], "apiProvider").String(); strings.Contains(p, "ANTHROPIC") {
							to = "Anthropic"
						} else if strings.Contains(p, "OPENAI") {
							to = "OpenAI"
						}
						own = append(own, Option{Value: k, Icon: modelIcon("", gjson.GetBytes(custom[k], "modelName").String()), Direct: to})
					}
				}
				sort.Slice(own, func(i, j int) bool { return own[i].Value < own[j].Value })
				if c := cur["model"]; c != "" && !usesMagpie(c) && custom[c] == nil {
					own = append([]Option{{Value: c, Icon: modelIcon("", c), Direct: "Google"}}, own...)
				}
				return append(group("Antigravity CLI", own), viaMagpie("agy", magpieID+"/")...)
			},
		}},
	}
}

// what agy says after a change (notice.go)
var noticeAgy = newNotice("agy takes magpie's gateway and model only from how it is started — start it with {command}")
