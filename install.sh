#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
bin_dir=${BROWSERKIT_BIN_DIR:-"${HOME}/.local/bin"}
skill_root=${BROWSERKIT_SKILL_DIR:-"${CODEX_HOME:-${HOME}/.codex}/skills/browserkit-cli"}

mkdir -p "$bin_dir" "$skill_root"
bin_dir=$(CDPATH= cd -- "$bin_dir" && pwd)
(cd "$repo_root" && go build -o "$bin_dir/browserkit" ./cmd/browserkit)
cp "$repo_root/skills/browserkit-cli/SKILL.md" "$skill_root/SKILL.md"
printf '已安装 browserkit: %s\n' "$bin_dir/browserkit"
printf '已安装 Skill: %s\n' "$skill_root/SKILL.md"
case ":${PATH}:" in
  *:"$bin_dir":*) ;;
  *) printf '请将 %s 加入 PATH。\n' "$bin_dir" ;;
esac
