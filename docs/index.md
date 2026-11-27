# Kubernetes libraries

The Kubernetes packages provide typed Unobin resources grouped by Kubernetes API
group and version, plus a dynamic library for built-in and custom objects. Import
the packages a factory uses and share one Kubernetes configuration between their
aliases when they connect to the same cluster.

```
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

## Configuration

All packages use
`library-config('github.com/cloudboss/unobin-library-kubernetes//config')`.
Configuration follows client-go loading rules: `KUBECONFIG` selects kubeconfig
files to merge, and `~/.kube/config` is the default when it is unset. An empty
configuration also supports in-cluster ServiceAccount credentials.

The library supports kubeconfig contexts, certificates and keys, bearer tokens,
OIDC, and exec credential plugins. Paths and inline certificate data retain
kubeconfig's conventions. Exec helpers must be available to the compiled factory.

Pass a configuration input to several aliases to use the same cluster:

```
inputs: {
  kubernetes-config: {
    type: library-config('github.com/cloudboss/unobin-library-kubernetes//config')
    default: {}
  }
}

imports: {
  core: 'github.com/cloudboss/unobin-library-kubernetes//api/core/v1'
  apps: 'github.com/cloudboss/unobin-library-kubernetes//api/apps/v1'
}

library-configs: {
  core: input.kubernetes-config
  apps: input.kubernetes-config
}
```

See the [configuration reference](reference/core-v1/configuration.md) for all
fields and sensitivity declarations.

## Typed resources

Typed libraries expose explicit schemas and resource-specific mutation and wait
behavior. Namespaced typed resources default to the `default` namespace unless
their metadata selects another namespace. Cluster-scoped resources use their
cluster endpoint.

The generated reference lists each registered resource, its inputs and outputs,
defaults, constraints, and sensitive fields. Its descriptions come from Go
documentation comments next to the implementation.

- [Core v1](reference/core-v1/index.md)
- [Apps v1](reference/apps-v1/index.md)
- [All libraries](reference/index.md)

## Dynamic objects

The [dynamic library](reference/dynamic/index.md) accepts a structured manifest
for a built-in or custom Kubernetes object. It uses server-side apply with field
ownership, conflict controls, and UID protection. CRDs must be established and
discoverable before their custom objects are applied.

Use the [object reference](reference/dynamic/resources/object.md) for its schema
and lifecycle controls. See [architecture](architecture.md) for implementation
ownership and API group boundaries.
