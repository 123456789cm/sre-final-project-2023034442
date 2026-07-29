#!/usr/bin/env bash
set -euo pipefail
[[ $# -eq 1 ]] || { echo "Usage: $0 <Gitee username or full repository URL>" >&2; exit 2; }
value=$1
if [[ "$value" == http://* || "$value" == https://* ]]; then
  url=$value
else
  url="https://gitee.com/${value}/sre-final-project-template.git"
fi
sed -i -E "s#repoURL: .*#repoURL: \"${url}\"#" argocd/application.yaml
echo "[OK] repoURL -> $url"
grep -nE 'repoURL|targetRevision|path:' argocd/application.yaml
