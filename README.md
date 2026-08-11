# homelab-operator

A Kubernetes controller that publishes Services through a
[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/)
by annotation, for the cluster described in
[ceid1987/homelab](https://github.com/ceid1987/homelab).

Annotate a Service with the hostname it should answer on:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: homelab-web
  annotations:
    homelab.carleid.dev/domain: homelab.carleid.dev
```

The operator adds a matching rule to the cloudflared ingress list, pointing at that Service's
in-cluster address. Remove the annotation, or the Service, and the rule goes with it.

## How it works

The controller watches Services carrying `homelab.carleid.dev/domain`. On any change it lists
every annotated Service in the cluster and rebuilds the tunnel's entire ingress list from
scratch, rather than patching individual rules:

1. Read the cloudflared `ConfigMap` and parse `config.yaml`.
2. Build one ingress rule per annotated Service, sorted by hostname, resolving the backend from
   the Service's real name, namespace and first port.
3. Append the catch-all, which must stay last or every unmatched hostname would 404.
4. Write the ConfigMap only if the result differs, then stamp a digest of it onto the
   cloudflared pod template so the tunnel rolls and reads the new config.

Rebuilding wholesale means deleted Services and removed annotations need no special handling,
and no finalizer is required — the config converges on cluster state from any starting point.
Top-level keys the operator does not manage (`tunnel`, `metrics`, `no-autoupdate`) and per-rule
extras such as `originRequest` are preserved across a rebuild.

**Deliberately not handled:** creating namespaces or ArgoCD `Application`s. ArgoCD does both
natively — `CreateNamespace=true` and an `ApplicationSet` — so the operator does only the part
Argo cannot.

### Configuration

| Environment variable | Default | Purpose |
|---|---|---|
| `CLOUDFLARED_CONFIGMAP` | `cloudflared` | ConfigMap holding `config.yaml` |
| `CLOUDFLARED_DEPLOYMENT` | `cloudflared` | Deployment to roll after a config change |
| `CLOUDFLARED_NAMESPACE` | `apps` | Namespace of both |

Two Services claiming the same hostname is a misconfiguration. The lower `namespace/name` wins,
deterministically, and the conflict is logged rather than flapping between claimants.

## Deployment

CI builds `ghcr.io/carleid-homelab/homelab-operator` on every push to `main` and publishes a
rendered `dist/install.yaml` to the `deploy` branch, which ArgoCD syncs. The `deploy` branch is
build output — regenerated from `main` on every run, never edited by hand.

To install it manually into any cluster:

```sh
kubectl apply -f https://raw.githubusercontent.com/carleid-homelab/homelab-operator/deploy/dist/install.yaml
```

## Development

```sh
make test                 # unit tests
make lint                 # golangci-lint
make docker-build docker-push IMG=<registry>/homelab-operator:tag
make deploy IMG=<registry>/homelab-operator:tag
make undeploy
```

The route-building logic is pure — it takes a slice of Services and returns ingress rules — so
`go test ./internal/...` covers sorting, port derivation, extras preservation, duplicate
resolution and YAML round-tripping without a cluster. The e2e suite (`make test-e2e`) needs a
[kind](https://sigs.k8s.io/kind) cluster.

Run `make help` for all targets. Built with
[kubebuilder](https://book.kubebuilder.io/introduction.html); there is no CRD, so `make install`
and `make uninstall` are no-ops.

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
