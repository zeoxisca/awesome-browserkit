package browserkit

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallScriptAgentTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh requires a POSIX shell")
	}
	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name  string
		args  []string
		env   []string
		paths []string
	}{
		{name: "default codex", paths: []string{"codex/skills/browserkit-cli/SKILL.md"}},
		{name: "pi", args: []string{"--agent", "pi"}, paths: []string{"pi/skills/browserkit-cli/SKILL.md"}},
		{name: "all", args: []string{"--agent", "all"}, paths: []string{"codex/skills/browserkit-cli/SKILL.md", "pi/skills/browserkit-cli/SKILL.md"}},
		{name: "environment default", env: []string{"BROWSERKIT_AGENT=pi"}, paths: []string{"pi/skills/browserkit-cli/SKILL.md"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			fakeBin := filepath.Join(root, "fake-bin")
			if err := os.MkdirAll(fakeBin, 0o755); err != nil {
				t.Fatal(err)
			}
			fakeGo := filepath.Join(fakeBin, "go")
			if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\nwhile [ \"$#\" -gt 0 ]; do\n  if [ \"$1\" = -o ]; then mkdir -p \"$(dirname \"$2\")\"; : > \"$2\"; exit 0; fi\n  shift\ndone\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/sh", append([]string{filepath.Join(repo, "install.sh")}, test.args...)...)
			cmd.Env = append(os.Environ(),
				"HOME="+root,
				"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"BROWSERKIT_AGENT=",
				"BROWSERKIT_BIN_DIR="+filepath.Join(root, "bin"),
				"BROWSERKIT_SKILL_DIR=",
				"BROWSERKIT_CODEX_SKILL_DIR=",
				"BROWSERKIT_PI_SKILL_DIR=",
				"CODEX_HOME="+filepath.Join(root, "codex"),
				"PI_CODING_AGENT_DIR="+filepath.Join(root, "pi"),
			)
			cmd.Env = append(cmd.Env, test.env...)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("install failed: %v\n%s", err, output)
			}
			for _, path := range test.paths {
				contents, err := os.ReadFile(filepath.Join(root, path))
				if err != nil {
					t.Fatalf("missing %s: %v\n%s", path, err, output)
				}
				if !strings.Contains(string(contents), "name: browserkit-cli") {
					t.Fatalf("invalid Skill at %s", path)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "bin", "browserkit")); err != nil {
				t.Fatalf("missing binary: %v", err)
			}
		})
	}
}

func TestInstallScriptRejectsUnknownAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh requires a POSIX shell")
	}
	cmd := exec.Command("/bin/sh", "./install.sh", "--agent", "unknown")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "不支持的 Agent") {
		t.Fatalf("expected agent validation error, err=%v output=%s", err, output)
	}
}

func TestInstallScriptRejectsRemovedBothAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh requires a POSIX shell")
	}
	cmd := exec.Command("/bin/sh", "./install.sh", "--agent", "both")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "不支持的 Agent") {
		t.Fatalf("expected removed alias to be rejected, err=%v output=%s", err, output)
	}
}
