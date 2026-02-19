#!/usr/bin/env python3
"""
homer-sync: Automatically generate Homer dashboard config from Kubernetes HTTPRoutes.

Scans HTTPRoutes cluster-wide, applies configurable filters, reads metadata from
annotations (on both HTTPRoutes and Namespaces), and renders a Homer config.yml
into a ConfigMap.
"""

import hashlib
import logging
import os
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Optional

import yaml
from jinja2 import Environment, FileSystemLoader, PackageLoader, select_autoescape
from kubernetes import client, config as k8s_config
from kubernetes.client.exceptions import ApiException

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

ANNOTATION_PREFIX = "home.mirceanton.com"
SA_NAMESPACE_FILE = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
BUILTIN_TEMPLATES_DIR = Path(__file__).parent / "templates"
BUILTIN_TEMPLATE_NAME = "config.yaml.j2"


def _parse_list(value: str) -> list[str]:
    """Parse a comma-separated env var into a stripped, non-empty list."""
    return [v.strip() for v in value.split(",") if v.strip()]


def _detect_namespace() -> str:
    """Return the pod's own namespace, falling back to 'default'."""
    try:
        return Path(SA_NAMESPACE_FILE).read_text().strip()
    except OSError:
        return "default"


@dataclass
class Config:
    gateway_names: list[str]
    domain_suffixes: list[str]
    configmap_name: str
    configmap_namespace: str
    daemon_mode: bool
    scan_interval: int
    log_level: str
    title: str
    subtitle: str
    columns: int
    template_path: str

    @classmethod
    def from_env(cls) -> "Config":
        return cls(
            gateway_names=_parse_list(os.environ.get("HOMER_SYNC_GATEWAY_NAMES", "")),
            domain_suffixes=_parse_list(os.environ.get("HOMER_SYNC_DOMAIN_SUFFIXES", "")),
            configmap_name=os.environ.get("HOMER_SYNC_CONFIGMAP_NAME", "homer-config"),
            configmap_namespace=os.environ.get("HOMER_SYNC_CONFIGMAP_NAMESPACE", "") or _detect_namespace(),
            daemon_mode=os.environ.get("HOMER_SYNC_DAEMON_MODE", "true").lower() == "true",
            scan_interval=int(os.environ.get("HOMER_SYNC_SCAN_INTERVAL", "300")),
            log_level=os.environ.get("HOMER_SYNC_LOG_LEVEL", "INFO").upper(),
            title=os.environ.get("HOMER_SYNC_TITLE", "Home Dashboard"),
            subtitle=os.environ.get("HOMER_SYNC_SUBTITLE", ""),
            columns=int(os.environ.get("HOMER_SYNC_COLUMNS", "5")),
            template_path=os.environ.get("HOMER_SYNC_TEMPLATE_PATH", ""),
        )

    @property
    def has_filters(self) -> bool:
        return bool(self.gateway_names or self.domain_suffixes)


# ---------------------------------------------------------------------------
# Data model
# ---------------------------------------------------------------------------

@dataclass
class ServiceItem:
    name: str
    subtitle: str
    url: str
    icon: str            # bare name, e.g. "jellyfin"; empty string means no logo
    group: str           # resolved group display name
    group_icon: str      # Font Awesome class for the group
    sort: int = 0


# ---------------------------------------------------------------------------
# Kubernetes helpers
# ---------------------------------------------------------------------------

def load_k8s_client() -> None:
    """Load in-cluster config, falling back to local kubeconfig."""
    try:
        k8s_config.load_incluster_config()
        logging.debug("Loaded in-cluster Kubernetes config")
    except k8s_config.ConfigException:
        k8s_config.load_kube_config()
        logging.debug("Loaded local kubeconfig")


def fetch_namespaces(core_v1: client.CoreV1Api) -> dict[str, dict]:
    """Return a map of namespace name → annotations dict (cached once per call)."""
    ns_map: dict[str, dict] = {}
    try:
        ns_list = core_v1.list_namespace()
        for ns in ns_list.items:
            ns_map[ns.metadata.name] = ns.metadata.annotations or {}
    except ApiException as e:
        logging.warning("Failed to list namespaces: %s", e)
    return ns_map


def namespace_group_name(ns_name: str, ns_annotations: dict) -> str:
    """Derive the group display name from namespace annotations or the name itself."""
    if override := ns_annotations.get(f"{ANNOTATION_PREFIX}/group"):
        return override
    # Title-case the namespace name, replacing hyphens with spaces
    return ns_name.replace("-", " ").title()


def namespace_group_icon(ns_annotations: dict) -> str:
    """Derive the group icon from namespace annotations."""
    return ns_annotations.get(f"{ANNOTATION_PREFIX}/group-icon", "fas fa-globe")


def fetch_httproutes(custom: client.CustomObjectsApi) -> list[dict]:
    """List all HTTPRoutes across all namespaces."""
    try:
        result = custom.list_cluster_custom_object(
            group="gateway.networking.k8s.io",
            version="v1",
            plural="httproutes",
        )
        return result.get("items", [])
    except ApiException as e:
        logging.error("Failed to list HTTPRoutes: %s", e)
        return []


# ---------------------------------------------------------------------------
# Filtering
# ---------------------------------------------------------------------------

def should_include(route: dict, cfg: Config) -> bool:
    """Determine whether an HTTPRoute should appear in the Homer dashboard."""
    annotations: dict = route.get("metadata", {}).get("annotations") or {}
    enabled_annotation = annotations.get(f"{ANNOTATION_PREFIX}/enabled", "").lower()

    if cfg.has_filters:
        # Opt-out mode: include unless explicitly disabled
        if enabled_annotation == "false":
            logging.debug(
                "Excluding %s/%s: disabled by annotation",
                route["metadata"]["namespace"],
                route["metadata"]["name"],
            )
            return False

        # Gateway filter
        if cfg.gateway_names:
            parent_refs = route.get("spec", {}).get("parentRefs") or []
            matched = any(ref.get("name") in cfg.gateway_names for ref in parent_refs)
            if not matched:
                logging.debug(
                    "Excluding %s/%s: no matching gateway in %s",
                    route["metadata"]["namespace"],
                    route["metadata"]["name"],
                    cfg.gateway_names,
                )
                return False

        # Domain suffix filter
        if cfg.domain_suffixes:
            hostnames = route.get("spec", {}).get("hostnames") or []
            matched = any(
                hostname.endswith(suffix)
                for hostname in hostnames
                for suffix in cfg.domain_suffixes
            )
            if not matched:
                logging.debug(
                    "Excluding %s/%s: no hostname matches suffixes %s",
                    route["metadata"]["namespace"],
                    route["metadata"]["name"],
                    cfg.domain_suffixes,
                )
                return False

        return True
    else:
        # Opt-in mode: only include if explicitly enabled
        return enabled_annotation == "true"


# ---------------------------------------------------------------------------
# Item extraction
# ---------------------------------------------------------------------------

def extract_item(
    route: dict,
    ns_map: dict[str, dict],
    group_icon_cache: dict[str, str],
) -> Optional[ServiceItem]:
    """Build a ServiceItem from an HTTPRoute and its namespace metadata."""
    meta = route.get("metadata", {})
    spec = route.get("spec", {})
    annotations: dict = meta.get("annotations") or {}
    ns_name: str = meta.get("namespace", "default")
    route_name: str = meta.get("name", "")

    # Determine URL from first hostname
    hostnames = spec.get("hostnames") or []
    if not hostnames:
        logging.warning("Skipping %s/%s: no hostnames defined", ns_name, route_name)
        return None
    url = f"https://{hostnames[0]}"

    # Resolve group: HTTPRoute annotation → namespace annotation → namespace name
    ns_annotations = ns_map.get(ns_name, {})
    if group_override := annotations.get(f"{ANNOTATION_PREFIX}/group"):
        group = group_override
        # For the icon, we try to find a namespace whose group name matches the override.
        # If found, use that namespace's icon; otherwise fall back to default.
        if group not in group_icon_cache:
            # Search all namespaces for a matching group name
            matched_icon = "fas fa-globe"
            for other_ns_annotations in ns_map.values():
                candidate = namespace_group_name(ns_name, other_ns_annotations)
                if candidate == group:
                    matched_icon = namespace_group_icon(other_ns_annotations)
                    break
            group_icon_cache[group] = matched_icon
    else:
        group = namespace_group_name(ns_name, ns_annotations)
        if group not in group_icon_cache:
            group_icon_cache[group] = namespace_group_icon(ns_annotations)

    return ServiceItem(
        name=annotations.get(f"{ANNOTATION_PREFIX}/name", route_name),
        subtitle=annotations.get(f"{ANNOTATION_PREFIX}/subtitle", ""),
        url=url,
        icon=annotations.get(f"{ANNOTATION_PREFIX}/icon", ""),
        group=group,
        group_icon=group_icon_cache[group],
        sort=int(annotations.get(f"{ANNOTATION_PREFIX}/sort", "0")),
    )


# ---------------------------------------------------------------------------
# Rendering
# ---------------------------------------------------------------------------

def resolve_template_env(cfg: Config) -> tuple[Environment, str]:
    """Return a Jinja2 Environment and template name based on config."""
    custom_path = cfg.template_path
    if custom_path and Path(custom_path).is_file():
        template_dir = str(Path(custom_path).parent)
        template_name = Path(custom_path).name
        logging.info("Using custom template: %s", custom_path)
        env = Environment(
            loader=FileSystemLoader(template_dir),
            autoescape=False,
            trim_blocks=True,
            lstrip_blocks=True,
        )
        return env, template_name

    logging.debug("Using built-in template")
    env = Environment(
        loader=FileSystemLoader(str(BUILTIN_TEMPLATES_DIR)),
        autoescape=False,
        trim_blocks=True,
        lstrip_blocks=True,
    )
    return env, BUILTIN_TEMPLATE_NAME


def render_config(items: list[ServiceItem], cfg: Config) -> str:
    """Render the Homer config.yml from collected items."""
    # Group items: group_name → sorted list of ServiceItem
    groups: dict[str, list[ServiceItem]] = {}
    for item in items:
        groups.setdefault(item.group, []).append(item)
    for group_items in groups.values():
        group_items.sort(key=lambda i: (i.sort, i.name))

    env, template_name = resolve_template_env(cfg)
    template = env.get_template(template_name)

    return template.render(
        title=cfg.title,
        subtitle=cfg.subtitle,
        columns=cfg.columns,
        groups=groups,
    )


# ---------------------------------------------------------------------------
# ConfigMap management
# ---------------------------------------------------------------------------

def _config_hash(content: str) -> str:
    return hashlib.sha256(content.encode()).hexdigest()


def sync_configmap(core_v1: client.CoreV1Api, cfg: Config, rendered: str) -> None:
    """Create or patch the homer-config ConfigMap only if content has changed."""
    name = cfg.configmap_name
    namespace = cfg.configmap_namespace

    try:
        existing = core_v1.read_namespaced_config_map(name=name, namespace=namespace)
        current_content = (existing.data or {}).get("config.yml", "")
        if _config_hash(current_content) == _config_hash(rendered):
            logging.debug("ConfigMap %s/%s is already up to date", namespace, name)
            return

        existing.data = {"config.yml": rendered}
        core_v1.replace_namespaced_config_map(name=name, namespace=namespace, body=existing)
        logging.info("Updated ConfigMap %s/%s", namespace, name)

    except ApiException as e:
        if e.status == 404:
            cm = client.V1ConfigMap(
                metadata=client.V1ObjectMeta(name=name, namespace=namespace),
                data={"config.yml": rendered},
            )
            core_v1.create_namespaced_config_map(namespace=namespace, body=cm)
            logging.info("Created ConfigMap %s/%s", namespace, name)
        else:
            logging.error("Failed to sync ConfigMap %s/%s: %s", namespace, name, e)
            raise


# ---------------------------------------------------------------------------
# Main loop
# ---------------------------------------------------------------------------

def run_once(core_v1: client.CoreV1Api, custom: client.CustomObjectsApi, cfg: Config) -> None:
    """Perform one full scan-render-sync cycle."""
    logging.info("Starting scan...")

    ns_map = fetch_namespaces(core_v1)
    routes = fetch_httproutes(custom)
    logging.debug("Found %d HTTPRoutes across the cluster", len(routes))

    group_icon_cache: dict[str, str] = {}
    items: list[ServiceItem] = []

    for route in routes:
        if not should_include(route, cfg):
            continue
        item = extract_item(route, ns_map, group_icon_cache)
        if item:
            items.append(item)

    logging.info("Collected %d services across %d groups", len(items), len({i.group for i in items}))

    rendered = render_config(items, cfg)
    sync_configmap(core_v1, cfg, rendered)
    logging.info("Scan complete.")


def main() -> None:
    cfg = Config.from_env()
    logging.basicConfig(
        level=getattr(logging, cfg.log_level, logging.INFO),
        format="%(asctime)s %(levelname)s %(message)s",
    )
    logging.info(
        "homer-sync starting (daemon=%s, interval=%ds, filters: gateways=%s domains=%s)",
        cfg.daemon_mode,
        cfg.scan_interval,
        cfg.gateway_names or "none",
        cfg.domain_suffixes or "none",
    )

    load_k8s_client()
    core_v1 = client.CoreV1Api()
    custom = client.CustomObjectsApi()

    if cfg.daemon_mode:
        while True:
            try:
                run_once(core_v1, custom, cfg)
            except Exception:
                logging.exception("Unhandled error during scan; will retry after interval")
            logging.debug("Sleeping %ds until next scan", cfg.scan_interval)
            time.sleep(cfg.scan_interval)
    else:
        run_once(core_v1, custom, cfg)


if __name__ == "__main__":
    main()
