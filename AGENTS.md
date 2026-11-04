# pvedisk contributor instructions

## Version every change

- Increase the root `VERSION` in every code, documentation, configuration, or test
  commit, and include the increase in that same commit.
- Use a patch increase by default. Use minor or major increases when Semantic
  Versioning requires them.
- Builds display the plain `VERSION` value. Keep the commit, build date, and build
  source in separate metadata fields; do not append development or commit suffixes.
- Build with `make build` or `sh scripts/build.sh` so metadata is stamped
  consistently. Release tags must match `VERSION`, with an optional `v` prefix.
- Run `make check` before committing. Plain `make` must continue to print help.
