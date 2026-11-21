# unobin-library-kubernetes

Kubernetes libraries for [Unobin](https://github.com/cloudboss/unobin).

Read the [full documentation](https://cloudboss.co/docs/unobin-library-kubernetes/).

The shared Kubernetes configuration is available at
`github.com/cloudboss/unobin-library-kubernetes//config`.

The Go module is `github.com/cloudboss/unobin-library-kubernetes`.

See [Architecture](docs/architecture.md) for public package ownership and API
group/version boundaries.

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

## Typed API resources

| Import | Resource |
| --- | --- |
| `github.com/cloudboss/unobin-library-kubernetes//api/core/v1` | `config-map`, `namespace`, `secret`, `service` |
| `github.com/cloudboss/unobin-library-kubernetes//api/apps/v1` | `deployment` |

Typed resources use Kubernetes create and JSON Patch operations. Namespaced
resources default to `default`; set metadata namespace to use another namespace.
Reads and deletion use the saved server and object identity with current
credentials. Resource outputs exclude volatile server metadata. Service outputs
include current load-balancer ingress endpoints; changes to those endpoints
refresh outputs without mutating the Service.

```ub
factory: {
  imports: {
    core: 'github.com/cloudboss/unobin-library-kubernetes//api/core/v1'
  }
  library-configs: { core: {} }
  resources: {
    settings: core.config-map {
      object: {
        metadata: { name: 'settings' }
        data: { greeting: 'hello' }
      }
    }
  }
  outputs: {
    name: { value: resource.settings.object.metadata.name }
  }
}
```

ConfigMap `binary-data` values use standard base64. With `immutable: true`,
changing text or binary data, or disabling immutability, replaces the ConfigMap.

Secret `object` inputs and outputs are sensitive ordinary state values. Text
`data` is computed when omitted and cleared by an explicit empty map. Selected
`binary-data` values use standard base64. Payload updates replace the complete
remote data map; immutable payload changes replace the Secret. Creation waits
for a service account token by default when the returned Secret has that type.
Metadata changes remain updates. Deletion returns when the API accepts the
request; finalizers can delay actual removal.

Deployment takes `metadata` and `spec` inputs, including a typed pod template.
It waits for rollout after create and update unless `wait-for-rollout` is false.
`create-timeout`, `update-timeout`, and `delete-timeout` accept positive Go
durations and default to `10m`. Deletion waits for the saved object to disappear.
Selector and other immutable-field changes require replacement; normal container
and replica changes update the existing Deployment.

## Dynamic objects

Import `github.com/cloudboss/unobin-library-kubernetes//dynamic` to manage built-in
or custom Kubernetes objects:

```ub
factory: {
  imports: {
    kubernetes: 'github.com/cloudboss/unobin-library-kubernetes//dynamic'
  }
  library-configs: { kubernetes: {} }
  resources: {
    settings: kubernetes.object {
      manifest: @core.to-json({
        apiVersion: 'v1'
        kind: 'ConfigMap'
        metadata: { name: 'settings' }
        data: { greeting: 'hello' }
      })
    }
  }
  outputs: {
    name: { value: resource.settings.name }
  }
}
```

`manifest` accepts one JSON or YAML object with `apiVersion`, `kind`, and
`metadata.name`. Namespaced objects use `metadata.namespace`, or the configured
namespace when it is absent. Cluster-scoped objects must omit the namespace.
CRDs must be established and discoverable before their custom objects are applied.

The resource uses server-side apply. `field-manager` defaults to `unobin`, and
`force-conflicts` defaults to `false`. Apply can adopt an existing object; conflicts
with another manager fail unless `force-conflicts` is enabled. Fields omitted
from an updated manifest follow Kubernetes field-ownership rules.

Changes to the API version, kind, name, namespace, or field manager require
replacement. Changes to explicit `server`, `kubeconfig`, `current-context`, or
configuration `namespace` also require replacement. Prior objects retain their
saved server and namespace for reads and deletion; current credentials are used.
Updates and deletes enforce the saved UID to protect a new object with the same
name. Deletion waits for the original object to disappear, including finalizers;
`delete-timeout` defaults to `2m` and accepts a positive Go duration such as `30s`.

Outputs include `server`, `api-version`, `kind`, `name`, `namespace`, and `uid`.
The sensitive `fields` and `values` outputs store the applied field selection and
its observed values for drift detection. Resource versions, timestamps, status,
and other managers' fields are excluded. The saved selection also detects changes
when another manager takes ownership of a field. Manifest text is sensitive too;
configure Unobin state encryption for manifests that contain secrets.

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

Run `make docs` to generate the reference manual from library schemas and Go
documentation comments. It uses the Cloudboss MkDocs theme. Generated files
under `docs/reference/` are excluded from Git.

Successful dependency downloads and builds create `_output/.deps` and
`_output/.build`. Make skips these commands while their inputs are unchanged.
Module changes trigger dependency downloads; project file changes trigger a
build. Tests, vet, and lint run each time they are requested.

Run `make test-integration-kind` for a temporary Kubernetes cluster or
`make test-integration-live` for an explicitly selected cluster. See
[Integration tests](tests/integration/README.md) for configuration and scenarios.

See [AGENTS.md](AGENTS.md) for repository conventions and [LICENSE](LICENSE) for
the MIT license.
