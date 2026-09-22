#!/bin/sh
set -eu

usage() {
	cat <<'EOF'
Usage: ./install.sh [--agent codex|pi|all]

Build browserkit and install its Agent Skill. The default agent is codex.

Environment:
  BROWSERKIT_AGENT             Default agent when --agent is omitted.
  BROWSERKIT_BIN_DIR           Binary destination (default: ~/.local/bin).
  BROWSERKIT_SKILL_DIR         Legacy override for one selected agent.
  BROWSERKIT_CODEX_SKILL_DIR   Codex Skill destination.
  BROWSERKIT_PI_SKILL_DIR      Pi Agent Skill destination.
  CODEX_HOME                   Codex data directory (default: ~/.codex).
  PI_CODING_AGENT_DIR          Pi Agent data directory (default: ~/.pi/agent).
EOF
}

agent=${BROWSERKIT_AGENT:-codex}
while [ "$#" -gt 0 ]; do
	case "$1" in
		--agent)
			[ "$#" -ge 2 ] || { printf '%s\n' '缺少 --agent 参数。' >&2; usage >&2; exit 2; }
			agent=$2
			shift 2
			;;
		--help|-h)
			usage
			exit 0
			;;
		*)
			printf '未知参数: %s\n' "$1" >&2
			usage >&2
			exit 2
			;;
	esac
done

case "$agent" in
	codex|pi|all) ;;
	*)
		printf '不支持的 Agent: %s（可选 codex、pi、all）\n' "$agent" >&2
		exit 2
		;;
esac

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
bin_dir=${BROWSERKIT_BIN_DIR:-"${HOME}/.local/bin"}

mkdir -p "$bin_dir"
bin_dir=$(CDPATH= cd -- "$bin_dir" && pwd)
(cd "$repo_root" && go build -o "$bin_dir/browserkit" ./cmd/browserkit)

install_skill() {
	skill_root=$1
	mkdir -p "$skill_root"
	skill_root=$(CDPATH= cd -- "$skill_root" && pwd)
	cp "$repo_root/skills/browserkit-cli/SKILL.md" "$skill_root/SKILL.md"
	printf '已为 %s 安装 Skill: %s\n' "$2" "$skill_root/SKILL.md"
}

legacy_skill_root=${BROWSERKIT_SKILL_DIR:-}
if [ "$agent" = codex ] || [ "$agent" = all ]; then
	codex_skill_root=${BROWSERKIT_CODEX_SKILL_DIR:-${legacy_skill_root:-"${CODEX_HOME:-${HOME}/.codex}/skills/browserkit-cli"}}
	install_skill "$codex_skill_root" Codex
fi
if [ "$agent" = pi ] || [ "$agent" = all ]; then
	pi_skill_root=${BROWSERKIT_PI_SKILL_DIR:-${legacy_skill_root:-"${PI_CODING_AGENT_DIR:-${HOME}/.pi/agent}/skills/browserkit-cli"}}
	install_skill "$pi_skill_root" "Pi Agent"
fi

printf '已安装 browserkit: %s\n' "$bin_dir/browserkit"
case ":${PATH}:" in
  *:"$bin_dir":*) ;;
  *) printf '请将 %s 加入 PATH。\n' "$bin_dir" ;;
esac
