#!/usr/bin/env bash
# Keep PyPI's long description identical to the repository README.
set -euo pipefail
cd "$(dirname "$0")/.."

if [ "${1:-}" = "--check" ]; then
  if ! cmp -s README.md python/README.md; then
    echo "::error::python/README.md is out of date: run scripts/sync_python_readme.sh" >&2
    exit 1
  fi
  echo "python/README.md matches README.md"
else
  cp README.md python/README.md
  echo "copied README.md to python/README.md"
fi
