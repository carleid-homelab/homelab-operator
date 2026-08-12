# homelab-operator

A Kubernetes controller that publishes Services through a
[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/)
by annotation. Built for the cluster described in
[carleid-homelab/homelab](https://github.com/carleid-homelab/homelab), but it has no dependency on
that setup beyond a cloudflared Deployment reading its config from a ConfigMap.

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
every annotated Service in the cluster and rebuilds the tunnel's entire ingress list from scratch,
rather than patching individual rules:

1. Read the cloudflared `ConfigMap` and parse `config.yaml`.
2. Build one ingress rule per annotated Service, sorted by hostname, resolving the backend from
   the Service's real name, namespace and first port.
3. Append the catch-all, which must stay last or every unmatched hostname would 404.
4. Write the ConfigMap only if the result differs, then stamp a digest of it onto the cloudflared
   pod template so the tunnel rolls and reads the new config.

## Configuration

| Environment variable | Default | Purpose |
|---|---|---|
| `CLOUDFLARED_CONFIGMAP` | `cloudflared` | ConfigMap holding `config.yaml` |
| `CLOUDFLARED_DEPLOYMENT` | `cloudflared` | Deployment to roll after a config change |
| `CLOUDFLARED_NAMESPACE` | `apps` | Namespace of both |

## Deployment

CI builds `ghcr.io/carleid-homelab/homelab-operator` on every push to `main`, runs
`make build-installer` with the image pinned to that commit, and force-pushes the rendered
`dist/install.yaml` to the `deploy` branch. The `deploy` branch is build output that happens to
live in Git: regenerated from `main` on every run, never edited by hand.

## Development

```sh
make test                 # unit tests
make lint                 # golangci-lint
make docker-build docker-push IMG=<registry>/homelab-operator:tag
make deploy IMG=<registry>/homelab-operator:tag
make undeploy
```
