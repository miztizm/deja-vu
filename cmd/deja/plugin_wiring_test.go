package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// pluginHooks is the hooks object a plugin bundle carries, in the shape
// Claude Code's settings use.
type pluginHooks map[string][]struct {
	Matcher string `json:"matcher"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	} `json:"hooks"`
}

type pluginMCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// A plugin bundle is what a marketplace install gets instead of
// `deja install <harness>-auto`, so it has to carry the same wiring: the same
// events, in the same order, with the same matchers and hook subcommands; a
// server that starts; files the manifest points at that exist; and a hook
// bridge that stands down when the installer wired the harness already. Its
// version moves with the other plugins.
func TestPluginMatchesTheInstaller(t *testing.T) {
	for _, tc := range []struct {
		name     string
		dir      string // the bundle, under the repo root
		manifest string // under dir
		// hooksFile holds the hooks object, under dir; empty when the
		// manifest carries it under "hooks".
		hooksFile string
		rootVar   string // what the bundle's commands name its root by
		timeout   int
		wiring    []struct{ Event, Sub, Matcher string }
		mcp       func(map[string]pluginMCPServer) bool
		skills    string // the manifest's skills field
		// bridge holds what hooks/deja.sh has to contain.
		bridge []string
		// files have to be regular files in the bundle; exec also executable.
		files, exec []string
	}{
		{
			name:     "codebuddy",
			dir:      "codebuddy-plugin",
			manifest: ".codebuddy-plugin/plugin.json",
			rootVar:  "CODEBUDDY_PLUGIN_ROOT",
			timeout:  codeBuddyHookTimeout,
			wiring:   codeBuddyHookWiring,
			mcp: func(s map[string]pluginMCPServer) bool {
				return s["deja"].Command == "${CODEBUDDY_PLUGIN_ROOT}/mcp/deja-mcp.sh"
			},
			skills: "./skills/",
			// It reads CodeBuddy's settings rather than Claude Code's.
			bridge: []string{`${CODEBUDDY_CONFIG_DIR:-$HOME/.codebuddy}/settings.json`},
			files:  []string{"skills/deja-history/SKILL.md", "hooks/deja.sh", "mcp/deja-mcp.sh"},
			exec:   []string{"hooks/deja.sh", "mcp/deja-mcp.sh"},
		},
		{
			name:      "devin",
			dir:       "devin-plugin",
			manifest:  ".devin-plugin/plugin.json",
			hooksFile: "hooks.json",
			rootVar:   "DEVIN_PLUGIN_ROOT",
			timeout:   60,
			wiring:    devinHookWiring,
			mcp: func(s map[string]pluginMCPServer) bool {
				d, ok := s["deja"]
				return ok && d.Command == "deja" && len(d.Args) == 1 && d.Args[0] == "mcp"
			},
			skills: "./skills/",
			// The guards the inline commands used to carry: stand down when
			// `deja install devin-auto` wired config.json already — every
			// session would otherwise get the digest twice — stay silent
			// without the binary, and say once, through the opening digest,
			// how to get it.
			bridge: []string{"config.json", "command -v deja", "systemMessage", "exec \"$DEJA\" \"$@\""},
			files: []string{
				"skills/deja-history/SKILL.md", "README.md", "LICENSE", "SECURITY.md",
				"hooks.json", "hooks/deja.sh",
			},
			exec: []string{"hooks/deja.sh"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m struct {
				Name       string                     `json:"name"`
				Version    string                     `json:"version"`
				Skills     string                     `json:"skills"`
				Hooks      pluginHooks                `json:"hooks"`
				MCPServers map[string]pluginMCPServer `json:"mcpServers"`
			}
			if err := json.Unmarshal(repoFile(t, tc.dir+"/"+tc.manifest), &m); err != nil {
				t.Fatal(err)
			}
			var claude struct {
				Version string `json:"version"`
			}
			if err := json.Unmarshal(repoFile(t, "claude-plugin/.claude-plugin/plugin.json"), &claude); err != nil {
				t.Fatal(err)
			}
			if m.Name != "deja-vu" || m.Version == "" || m.Version != claude.Version {
				t.Fatalf("name %q version %q, want deja-vu at %q", m.Name, m.Version, claude.Version)
			}
			if !tc.mcp(m.MCPServers) {
				t.Errorf("mcpServers = %+v", m.MCPServers)
			}
			if m.Skills != tc.skills {
				t.Errorf("skills field = %q, want %q", m.Skills, tc.skills)
			}

			hooks := m.Hooks
			if tc.hooksFile != "" {
				if len(hooks) != 0 {
					t.Errorf("manifest carries hooks of its own beside %s", tc.hooksFile)
				}
				if err := json.Unmarshal(repoFile(t, tc.dir+"/"+tc.hooksFile), &hooks); err != nil {
					t.Fatal(err)
				}
			}
			want := map[string][]int{}
			for i, w := range tc.wiring {
				want[w.Event] = append(want[w.Event], i)
			}
			bridge := `"${` + tc.rootVar + `}/hooks/deja.sh" `
			for event, idx := range want {
				entries := hooks[event]
				if len(entries) != len(idx) {
					t.Fatalf("%s: plugin wires %d entries, the installer %d", event, len(entries), len(idx))
				}
				for i, wi := range idx {
					w := tc.wiring[wi]
					e := entries[i]
					if e.Matcher != w.Matcher || len(e.Hooks) != 1 {
						t.Fatalf("%s[%d]: entry %+v, want matcher %q", event, i, e, w.Matcher)
					}
					h := e.Hooks[0]
					if h.Type != "command" || h.Timeout != tc.timeout {
						t.Fatalf("%s[%d]: hook %+v, want a command with timeout %d", event, i, h, tc.timeout)
					}
					if h.Command != bridge+w.Sub {
						t.Fatalf("%s[%d]: command %q does not bridge to deja %s", event, i, h.Command, w.Sub)
					}
				}
			}
			// An event the plugin still wires after the installer dropped it
			// would keep firing for marketplace installs only.
			for event := range hooks {
				if _, ok := want[event]; !ok {
					t.Errorf("plugin wires %s, which the installer never does", event)
				}
			}

			b := string(repoFile(t, tc.dir+"/hooks/deja.sh"))
			for _, s := range tc.bridge {
				if !strings.Contains(b, s) {
					t.Errorf("hooks/deja.sh is missing %q", s)
				}
			}
			root := filepath.Join("..", "..", tc.dir)
			for _, rel := range tc.files {
				info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
				if err != nil || !info.Mode().IsRegular() {
					t.Errorf("%s/%s: %v", tc.dir, rel, err)
				}
			}
			for _, rel := range tc.exec {
				info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
				if err != nil || (runtime.GOOS != "windows" && info.Mode()&0o111 == 0) {
					t.Errorf("%s/%s is not executable: %v", tc.dir, rel, err)
				}
			}
		})
	}
}
