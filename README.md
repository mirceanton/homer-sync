# homer-sync

> [!NOTE]
> This project is AI-generated ("AI slop"). The code and documentation were written with significant AI assistance. If that's not your thing, you have been warned.

Automatically generate a [Homer](https://github.com/bastienwirtz/homer) dashboard config from Kubernetes HTTPRoutes.

`homer-sync` scans `HTTPRoute` resources cluster-wide, applies configurable filters, reads metadata from annotations on both routes and namespaces, and renders a Homer `config.yml` into a ConfigMap — keeping your dashboard in sync without manual edits.

## How it works

1. Fetches all `HTTPRoute` resources across the cluster (Gateway API `v1`)
2. Filters them based on gateway names and/or domain suffixes (if configured)
3. Reads display metadata from annotations on routes and namespaces
4. Groups services by namespace, using namespace annotations for group names and icons
5. Renders a `config.yml` via a Jinja2 template
6. Creates or updates a ConfigMap with the rendered config

## Filtering modes

Filtering behavior depends on whether any filter env vars are set:

| Mode        | Condition                                   | Behavior                                                                                                  |
| ----------- | ------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| **Opt-in**  | No `GATEWAY_NAMES` or `DOMAIN_SUFFIXES` set | Only routes explicitly annotated with `home.mirceanton.com/enabled: "true"` are included                  |
| **Opt-out** | At least one filter is set                  | All routes matching the filters are included unless annotated with `home.mirceanton.com/enabled: "false"` |

## Annotations

### On `HTTPRoute`

| Annotation                     | Description                                                           | Default              |
| ------------------------------ | --------------------------------------------------------------------- | -------------------- |
| `home.mirceanton.com/enabled`  | `"true"` to opt in (opt-in mode), `"false"` to opt out (opt-out mode) | —                    |
| `home.mirceanton.com/name`     | Display name for the service                                          | HTTPRoute name       |
| `home.mirceanton.com/subtitle` | Subtitle shown under the service name                                 | `""`                 |
| `home.mirceanton.com/icon`     | Icon name (e.g. `jellyfin`); resolved to `assets/icons/<name>.svg`    | none                 |
| `home.mirceanton.com/group`    | Override the group this service belongs to                            | Namespace group name |
| `home.mirceanton.com/sort`     | Integer sort order within the group                                   | `0`                  |

### On `Namespace`

| Annotation                       | Description                           | Default                      |
| -------------------------------- | ------------------------------------- | ---------------------------- |
| `home.mirceanton.com/group`      | Display name for the group            | Namespace name (title-cased) |
| `home.mirceanton.com/group-icon` | Font Awesome class for the group icon | `fas fa-globe`               |

## Configuration

All configuration is via environment variables:

| Variable                         | Description                                                | Default             |
| -------------------------------- | ---------------------------------------------------------- | ------------------- |
| `HOMER_SYNC_GATEWAY_NAMES`       | Comma-separated gateway names to filter by                 | `""` (all)          |
| `HOMER_SYNC_DOMAIN_SUFFIXES`     | Comma-separated domain suffixes to filter by               | `""` (all)          |
| `HOMER_SYNC_CONFIGMAP_NAME`      | Name of the ConfigMap to write                             | `homer-config`      |
| `HOMER_SYNC_CONFIGMAP_NAMESPACE` | Namespace for the ConfigMap                                | Pod's own namespace |
| `HOMER_SYNC_DAEMON_MODE`         | Run continuously (`true`) or exit after one sync (`false`) | `true`              |
| `HOMER_SYNC_SCAN_INTERVAL`       | Seconds between scans in daemon mode                       | `300`               |
| `HOMER_SYNC_LOG_LEVEL`           | Log verbosity: `DEBUG`, `INFO`, `WARNING`, `ERROR`         | `INFO`              |
| `HOMER_SYNC_TITLE`               | Homer dashboard title                                      | `Home Dashboard`    |
| `HOMER_SYNC_SUBTITLE`            | Homer dashboard subtitle                                   | `""`                |
| `HOMER_SYNC_COLUMNS`             | Number of service columns in the layout                    | `5`                 |
| `HOMER_SYNC_TEMPLATE_PATH`       | Path to a custom Jinja2 template                           | built-in            |

### Custom template

If `HOMER_SYNC_TEMPLATE_PATH` points to a valid file, it is used instead of the built-in template. The template receives:

- `title` — dashboard title
- `subtitle` — dashboard subtitle
- `columns` — number of columns
- `groups` — dict of `group_name → list[ServiceItem]`, each item having `name`, `subtitle`, `url`, `icon`, `group`, `group_icon`, `sort`

## Installation

### Helm

```sh
helm install homer-sync oci://ghcr.io/mirceanton/charts/homer-sync
```

Override values as needed:

```yaml
env:
  HOMER_SYNC_GATEWAY_NAMES: "internal,external"
  HOMER_SYNC_DOMAIN_SUFFIXES: ".home.example.com"
  HOMER_SYNC_TITLE: "My Dashboard"
  HOMER_SYNC_SCAN_INTERVAL: "120"
```

### Docker

```sh
docker run \
  -e HOMER_SYNC_GATEWAY_NAMES=internal \
  -e HOMER_SYNC_DOMAIN_SUFFIXES=.home.example.com \
  ghcr.io/mirceanton/homer-sync:latest
```

## RBAC

The Helm chart creates a `ServiceAccount`, `ClusterRole`, and `ClusterRoleBinding` granting read access to `httproutes` (Gateway API) and `namespaces`.

## Example annotation setup

```yaml
# Namespace
apiVersion: v1
kind: Namespace
metadata:
  name: media
  annotations:
    home.mirceanton.com/group: "Media"
    home.mirceanton.com/group-icon: "fas fa-film"
---
# HTTPRoute
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: jellyfin
  namespace: media
  annotations:
    home.mirceanton.com/name: "Jellyfin"
    home.mirceanton.com/subtitle: "Media server"
    home.mirceanton.com/icon: "jellyfin"
    home.mirceanton.com/sort: "1"
```
