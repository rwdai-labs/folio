# CI and public image delivery

Inherit `../AGENTS.md`. `workflows/container.yml` owns Go validation, separate
bridge checks, runtime image smoke tests and GHCR image publication.

- Keep root-module and nested `plugins/github-bridge` validation separate.
  Source-path filters must cover affected modules/assets so relevant pull
  requests actually run checks; inspect filters when adding an area.
- Preserve the dependency chain: tests, runtime smoke, then image delivery.
  Keep pull requests from publishing; branch/tag publishing retains intended
  AMD64/ARM64 support, provenance and SBOM behaviour.
- Keep tool-version expectations synchronized with `deploy/Dockerfile` and its
  build-context allowlist. Smoke checks must verify installed tools/assets,
  non-root runtime and absence of private content, not just image existence.
- Do not print secrets or use real context/volumes in CI. Package metadata is
  not evidence of an anonymous pull; publication claims require actual evidence.
- Workflow/trigger/check changes require this file and deployment instructions
  to stay current in the same change. Document any validation gap explicitly.

Validate changed YAML with available workflow tooling (for example actionlint)
and run the affected local Go/container checks. A local check cannot establish
GitHub trigger, registry permissions or multi-architecture publish success;
inspect the actual CI run when delivery is part of the task.
