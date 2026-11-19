# Repository guidance

- Follow `../unobin-library-aws` closely for build, test, documentation, and CI
  conventions. Preserve its names where they apply.
- Keep the Makefile concise. Put any build helper scripts in `hack/`.
- Use Kubernetes and client-go configuration concepts and standard environment
  variables. Use strict kebab case for all Unobin fields and let the SDK derive
  their names. Preserve kubeconfig's file and inline-data conventions.
- Keep `.agents/` and `.codex/` external to the library. Do not include their
  dependencies or tests in the library's build or CI.
- Put Unobin test fixtures in package-local `testdata/ub/` files. Do not embed
  Unobin source in Go test strings.
- Test behavior. Do not add tests that inspect documentation or tooling text.
- Make discrete commits after the relevant tests pass. Use the `git-commit`
  skill and preserve unrelated changes.
