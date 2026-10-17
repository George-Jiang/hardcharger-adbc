#!/usr/bin/env bash
# Build the adbc-driver-warpsql wheel this package depends on, from the pinned
# third_party/warp-sql-adbc submodule, into build/, and write build/overrides.txt: a uv
# override that points the dependency at that wheel (adbc-driver-warpsql isn't on PyPI yet).
#
#   scripts/build_warpsql_wheel.sh
#   uv pip install --override build/overrides.txt -e "python[test,dev]"
#
# Needs Go and, on Linux, zig (the library targets glibc 2.17; see the submodule's
# scripts/build_lib.sh).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WARP="$ROOT/third_party/warp-sql-adbc"
[ -f "$WARP/scripts/build_lib.sh" ] || {
  echo "third_party/warp-sql-adbc is missing: run git submodule update --init" >&2
  exit 1
}
export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-11.0}"
"$WARP/scripts/build_lib.sh"

PKG="$WARP/python/src/adbc_driver_warpsql"
case "$(uname -s)" in
  Darwin) EXT=dylib ;;
  MINGW*|MSYS*|CYGWIN*) EXT=dll ;;
  *) EXT=so ;;
esac
cp "$WARP/go/build/libadbc_driver_warpsql.$EXT" "$PKG/"
trap 'rm -f "$PKG/libadbc_driver_warpsql.$EXT"' EXIT
mkdir -p "$ROOT/build"
rm -f "$ROOT"/build/adbc_driver_warpsql-*.whl
if command -v uv >/dev/null; then
  uv build --wheel --out-dir "$ROOT/build" "$WARP/python"
else
  python -m pip wheel --no-deps --wheel-dir "$ROOT/build" "$WARP/python"
fi
WHEEL="$(ls "$ROOT"/build/adbc_driver_warpsql-*.whl)"
[ -n "$WHEEL" ] || { echo "no wheel was built" >&2; exit 1; }
printf 'adbc-driver-warpsql @ file://%s\n' "$WHEEL" > "$ROOT/build/overrides.txt"
echo "built $WHEEL"
