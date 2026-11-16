# unobin-library-kubernetes

Kubernetes libraries for [Unobin](https://github.com/cloudboss/unobin).

This repository contains the initial project files. No resources, data sources,
or actions are implemented yet.

The Go module is `github.com/cloudboss/unobin-library-kubernetes`. It uses Go
1.26.2, matching `unobin-library-aws`.

## Development

Run the development targets with Docker and Make:

```sh
make deps
make build
make test
make vet
make lint
```

The targets use pinned Go and golangci-lint images. Containers run with the
current user's UID and GID, and keep their caches in `_output/`.

Successful dependency downloads and builds create `_output/.deps` and
`_output/.build`. Make skips these commands while their inputs are unchanged.
Module changes trigger dependency downloads; project file changes trigger a
build. Tests, vet, and lint run each time they are requested.

See [AGENTS.md](AGENTS.md) for repository conventions and [LICENSE](LICENSE) for
the MIT license.
