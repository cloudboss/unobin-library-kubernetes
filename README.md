# unobin-library-kubernetes

Kubernetes libraries for [Unobin](https://github.com/cloudboss/unobin).

The shared Kubernetes configuration is available at
`github.com/cloudboss/unobin-library-kubernetes//config`.

The Go module is `github.com/cloudboss/unobin-library-kubernetes`. It uses Go
1.26.2, matching `unobin-library-aws`.

## Configuration

Configuration uses client-go's loading rules: `KUBECONFIG` is a list of files
to merge, and `~/.kube/config` is the default when that variable is unset.
When configuration is empty, client-go tries the in-cluster service account.
Kubeconfig authentication includes client certificates, bearer tokens,
token files, OIDC, and exec credential plugins.

The library configuration fields override the selected kubeconfig context:

| Fields | Meaning |
| --- | --- |
| `kubeconfig` | Load one explicit file instead of the default search. |
| `current-context`, `namespace` | Select the context and default namespace. |
| `server`, `tls-server-name` | API server URL and TLS certificate name. |
| `certificate-authority`, `certificate-authority-data` | CA certificate path or base64 PEM. |
| `insecure-skip-tls-verify`, `disable-compression` | TLS verification and response compression options. |
| `proxy-url` | HTTP, HTTPS, or SOCKS5 proxy URL. Otherwise use client-go's proxy environment handling. |
| `client-certificate`, `client-certificate-data` | Client certificate path or base64 PEM. |
| `client-key`, `client-key-data` | Client private key path or base64 PEM. |
| `token`, `token-file` | Bearer token value or token file path. |
| `exec` | Exec credential plugin configuration. |
| `as`, `as-uid`, `as-groups`, `as-user-extra` | Kubernetes impersonation options. |

All fields are optional. File paths inside kubeconfig are relative to that file;
paths set directly in the library configuration are relative to the process's
working directory. Use one file or data field for each certificate or key.
The `*-data` fields accept base64-encoded PEM, as kubeconfig does.

The `exec` object uses `command`, `args`, `env`, `api-version`,
`interactive-mode`, `provide-cluster-info`, and `install-hint`. Each `env` item
has `name` and `value` fields. For `client.authentication.k8s.io/v1`, set
`interactive-mode` explicitly. Client-go runs the helper and manages credential
refresh. Helpers and their environment must be available where the factory runs.

An explicit credential replaces inherited user credentials. The `token` field can
reference an action output through an ordinary Unobin expression. The compiled
configuration test exercises this dependency through plan and apply. A token
value is a snapshot; use an exec helper or token file when refresh is required.

Tokens, private key data, and exec environment values are marked sensitive. Treat
plans and state as sensitive too, and configure Unobin state encryption.

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

Run `make test-integration-kind` for a temporary Kubernetes cluster or
`make test-integration-live` for an explicitly selected cluster. See
[Integration tests](tests/integration/README.md) for configuration and scenarios.

See [AGENTS.md](AGENTS.md) for repository conventions and [LICENSE](LICENSE) for
the MIT license.
