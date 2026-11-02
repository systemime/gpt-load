package state

import (
	"encoding/json"
	"testing"

	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/protocol"
)

// 强制改写: 命中键的路由目标整体替换为指向目标模型, 未命中的键与禁用状态不受影响。
func TestCompileAppliesForcedModelRewrite(t *testing.T) {
	compile := func(settings config.Settings) *ConfigSnapshot {
		t.Helper()
		snapshot, err := Compile(CompileInput{
			ChannelRegistry: channel.NewRegistry(),
			Groups: []GroupConfig{
				{ConnectionType: "api_key", ID: 1, ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`),
					Settings: settings,
					Models:   []ModelConfig{{ID: "glm-5.3"}, {ID: "glm-5.3-flash"}}, Enabled: true},
			},
		})
		if err != nil {
			t.Fatalf("Compile() error = %v", err)
		}
		return snapshot
	}

	bucket := func(snapshot *ConfigSnapshot, key string) []RouteTarget {
		return snapshot.ExecutionCandidates[protocol.OpenAICompletions][execution.OperationChatCompletion][key]
	}

	base := compile(config.Settings{})
	if got := bucket(base, "glm-5.3"); len(got) != 1 || got[0].UpstreamModelID != "glm-5.3" {
		t.Fatalf("baseline glm-5.3 targets = %#v", got)
	}

	rewrite := config.Settings{
		SettingForcedModelRewrite: map[string]any{
			"enabled": true, "mappings": map[string]any{"glm-5.3": "glm-5.3-flash"},
		},
	}
	forced := compile(rewrite)
	if got := bucket(forced, "glm-5.3"); len(got) != 1 || got[0].UpstreamModelID != "glm-5.3-flash" {
		t.Fatalf("forced glm-5.3 targets = %#v, want single upstream glm-5.3-flash", got)
	}
	if got := bucket(forced, "glm-5.3-flash"); len(got) != 1 || got[0].UpstreamModelID != "glm-5.3-flash" {
		t.Fatalf("target model own key = %#v, want untouched", got)
	}

	// 未启用: 配置保留在视图里(开关与映射都可回显), 路由不生效。
	disabled := compile(config.Settings{
		SettingForcedModelRewrite: map[string]any{
			"enabled": false, "mappings": map[string]any{"glm-5.3": "glm-5.3-flash"},
		},
	})
	if got := bucket(disabled, "glm-5.3"); len(got) != 1 || got[0].UpstreamModelID != "glm-5.3" {
		t.Fatalf("disabled rewrite should keep original routing: %#v", got)
	}
	view := disabled.Groups[1].ForcedModelRewrite
	if view.Enabled || view.Mappings["glm-5.3"] != "glm-5.3-flash" {
		t.Fatalf("disabled view should carry toggle-off config: %#v", view)
	}

	// 残留条目(模型已移除)编译期过滤, 分组照常编译。
	stale := compile(config.Settings{
		SettingForcedModelRewrite: map[string]any{
			"enabled": true,
			"mappings": map[string]any{"glm-5.3": "removed-model", "removed-source": "glm-5.3-flash"},
		},
	})
	if got := bucket(stale, "glm-5.3"); len(got) != 1 || got[0].UpstreamModelID != "glm-5.3" {
		t.Fatalf("stale target should be dropped: %#v", got)
	}
}

func TestParseForcedModelRewriteValidation(t *testing.T) {
	valid := map[string]any{"enabled": true, "mappings": map[string]any{"a": "b"}}
	if _, err := ParseForcedModelRewrite("forced_model_rewrite", valid); err != nil {
		t.Fatalf("valid parse error = %v", err)
	}
	for name, invalid := range map[string]any{
		"self mapping": map[string]any{"enabled": true, "mappings": map[string]any{"a": "a"}},
		"empty target": map[string]any{"enabled": true, "mappings": map[string]any{"a": "  "}},
		"bad enabled":  map[string]any{"enabled": "yes"},
		"mappings not object": map[string]any{
			"enabled": true, "mappings": []any{map[string]any{"from": "a", "to": "b"}},
		},
	} {
		if _, err := ParseForcedModelRewrite("forced_model_rewrite", invalid); err == nil {
			t.Fatalf("%s should be rejected", name)
		}
	}
}

func TestResolveGroupRuntimeSettingsForcedRewrite(t *testing.T) {
	resolved, err := ResolveGroupRuntimeSettings(DefaultRuntimeSettings(), config.Settings{
		SettingForcedModelRewrite: map[string]any{
			"enabled": true, "mappings": map[string]any{"glm-5.3": "glm-5.3-flash"},
		},
	})
	if err != nil {
		t.Fatalf("ResolveGroupRuntimeSettings() error = %v", err)
	}
	if got := resolved.ForcedModelRewrite.EffectiveForcedMappings(); got["glm-5.3"] != "glm-5.3-flash" {
		t.Fatalf("effective mappings = %#v", got)
	}
}
