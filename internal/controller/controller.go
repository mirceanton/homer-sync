package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mirceanton/homer-sync/internal/config"
	"github.com/mirceanton/homer-sync/internal/k8s"
)

// ServiceItem holds the resolved metadata for a single Homer dashboard entry.
type ServiceItem struct {
	Name      string
	Subtitle  string
	URL       string
	Icon      string
	Group     string
	GroupIcon string
	Sort      int
}

// Controller performs the scan→render→sync cycle.
type Controller struct {
	clients *k8s.Clients
	cfg     *config.Config
}

// New returns a Controller ready to run.
func New(clients *k8s.Clients, cfg *config.Config) *Controller {
	return &Controller{clients: clients, cfg: cfg}
}

// Run starts the controller. In daemon mode it loops indefinitely; otherwise it
// runs once and returns.
func (c *Controller) Run(ctx context.Context) error {
	if c.cfg.Daemon {
		for {
			if err := ctx.Err(); err != nil {
				return nil
			}
			if err := c.runOnce(ctx); err != nil {
				slog.Error("unhandled error during scan; will retry after interval", "error", err)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Duration(c.cfg.ScanInterval) * time.Second):
			}
		}
	}
	return c.runOnce(ctx)
}

// ---------------------------------------------------------------------------
// Single scan cycle
// ---------------------------------------------------------------------------

func (c *Controller) runOnce(ctx context.Context) error {
	slog.Info("starting scan")

	nsMap, err := c.fetchNamespaces(ctx)
	if err != nil {
		return fmt.Errorf("fetch namespaces: %w", err)
	}

	routes, err := c.fetchHTTPRoutes(ctx)
	if err != nil {
		return fmt.Errorf("fetch httproutes: %w", err)
	}
	slog.Debug("found httproutes", "count", len(routes))

	groupIconCache := make(map[string]string)
	var items []ServiceItem

	for _, route := range routes {
		if !c.shouldInclude(route) {
			continue
		}
		item, ok := c.extractItem(route, nsMap, groupIconCache)
		if ok {
			items = append(items, item)
		}
	}

	groups := make(map[string][]ServiceItem)
	for _, item := range items {
		groups[item.Group] = append(groups[item.Group], item)
	}
	for g := range groups {
		sort.Slice(groups[g], func(i, j int) bool {
			if groups[g][i].Sort != groups[g][j].Sort {
				return groups[g][i].Sort < groups[g][j].Sort
			}
			return groups[g][i].Name < groups[g][j].Name
		})
	}

	slog.Info("collected services", "services", len(items), "groups", len(groups))

	rendered, err := c.buildTemplateData(groups)
	if err != nil {
		return fmt.Errorf("render config: %w", err)
	}

	if err := c.syncConfigMap(ctx, rendered); err != nil {
		return fmt.Errorf("sync configmap: %w", err)
	}

	slog.Info("scan complete")
	return nil
}

// ---------------------------------------------------------------------------
// Kubernetes helpers
// ---------------------------------------------------------------------------

// namespaceAnnotations is the annotation map for a single namespace.
type namespaceAnnotations = map[string]string

func (c *Controller) fetchNamespaces(ctx context.Context) (map[string]namespaceAnnotations, error) {
	nsMap := make(map[string]namespaceAnnotations)
	list, err := c.clients.Core.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	for _, ns := range list.Items {
		ann := ns.Annotations
		if ann == nil {
			ann = make(map[string]string)
		}
		nsMap[ns.Name] = ann
	}
	return nsMap, nil
}

func (c *Controller) fetchHTTPRoutes(ctx context.Context) ([]map[string]interface{}, error) {
	list, err := c.clients.Gateway.GatewayV1().HTTPRoutes("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list httproutes: %w", err)
	}

	routes := make([]map[string]interface{}, 0, len(list.Items))
	for _, r := range list.Items {
		// Build a minimal map that mirrors the Python dict structure so we
		// can share the same annotation-processing logic.
		parentRefs := make([]map[string]interface{}, 0, len(r.Spec.ParentRefs))
		for _, pr := range r.Spec.ParentRefs {
			parentRefs = append(parentRefs, map[string]interface{}{
				"name": string(pr.Name),
			})
		}
		hostnames := make([]string, 0, len(r.Spec.Hostnames))
		for _, h := range r.Spec.Hostnames {
			hostnames = append(hostnames, string(h))
		}

		ann := r.Annotations
		if ann == nil {
			ann = make(map[string]string)
		}

		routes = append(routes, map[string]interface{}{
			"namespace":   r.Namespace,
			"name":        r.Name,
			"annotations": ann,
			"parentRefs":  parentRefs,
			"hostnames":   hostnames,
		})
	}
	return routes, nil
}

// ---------------------------------------------------------------------------
// Filtering
// ---------------------------------------------------------------------------

func (c *Controller) shouldInclude(route map[string]interface{}) bool {
	ann := routeAnnotations(route)
	enabled := strings.ToLower(ann[config.AnnotationPrefix+"/enabled"])
	ns := route["namespace"].(string)
	name := route["name"].(string)

	if c.cfg.HasFilters() {
		// Opt-out mode: include unless explicitly disabled.
		if enabled == "false" {
			slog.Debug("excluding route: disabled by annotation", "namespace", ns, "name", name)
			return false
		}

		if len(c.cfg.GatewayNames) > 0 {
			if !matchesGateway(route, c.cfg.GatewayNames) {
				slog.Debug("excluding route: no matching gateway", "namespace", ns, "name", name, "gateways", c.cfg.GatewayNames)
				return false
			}
		}

		if len(c.cfg.DomainSuffixes) > 0 {
			if !matchesDomainSuffix(route, c.cfg.DomainSuffixes) {
				slog.Debug("excluding route: no hostname matches suffixes", "namespace", ns, "name", name, "suffixes", c.cfg.DomainSuffixes)
				return false
			}
		}

		return true
	}

	// Opt-in mode: only include if explicitly enabled.
	return enabled == "true"
}

func matchesGateway(route map[string]interface{}, names []string) bool {
	refs, _ := route["parentRefs"].([]map[string]interface{})
	for _, ref := range refs {
		n, _ := ref["name"].(string)
		for _, want := range names {
			if n == want {
				return true
			}
		}
	}
	return false
}

func matchesDomainSuffix(route map[string]interface{}, suffixes []string) bool {
	hostnames, _ := route["hostnames"].([]string)
	for _, h := range hostnames {
		for _, s := range suffixes {
			if strings.HasSuffix(h, s) {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Item extraction
// ---------------------------------------------------------------------------

func (c *Controller) extractItem(
	route map[string]interface{},
	nsMap map[string]namespaceAnnotations,
	groupIconCache map[string]string,
) (ServiceItem, bool) {
	ann := routeAnnotations(route)
	ns := route["namespace"].(string)
	name := route["name"].(string)

	hostnames, _ := route["hostnames"].([]string)
	if len(hostnames) == 0 {
		slog.Warn("skipping route: no hostnames defined", "namespace", ns, "name", name)
		return ServiceItem{}, false
	}
	url := "https://" + hostnames[0]

	nsAnn := nsMap[ns]

	var group string
	if override, ok := ann[config.AnnotationPrefix+"/group"]; ok && override != "" {
		group = override
		if _, seen := groupIconCache[group]; !seen {
			groupIconCache[group] = resolveGroupIconForName(group, nsMap)
		}
	} else {
		group = namespaceGroupName(ns, nsAnn)
		if _, seen := groupIconCache[group]; !seen {
			groupIconCache[group] = namespaceGroupIcon(nsAnn)
		}
	}

	sortVal := 0
	if sv, ok := ann[config.AnnotationPrefix+"/sort"]; ok && sv != "" {
		fmt.Sscanf(sv, "%d", &sortVal)
	}

	return ServiceItem{
		Name:      stringOr(ann[config.AnnotationPrefix+"/name"], name),
		Subtitle:  ann[config.AnnotationPrefix+"/subtitle"],
		URL:       url,
		Icon:      ann[config.AnnotationPrefix+"/icon"],
		Group:     group,
		GroupIcon: groupIconCache[group],
		Sort:      sortVal,
	}, true
}

// ---------------------------------------------------------------------------
// Namespace helpers
// ---------------------------------------------------------------------------

func namespaceGroupName(ns string, ann map[string]string) string {
	if override, ok := ann[config.AnnotationPrefix+"/group"]; ok && override != "" {
		return override
	}
	return titleCase(strings.ReplaceAll(ns, "-", " "))
}

// titleCase capitalises the first letter of each space-separated word.
// It is used instead of the deprecated strings.Title.
func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

func namespaceGroupIcon(ann map[string]string) string {
	if icon, ok := ann[config.AnnotationPrefix+"/group-icon"]; ok && icon != "" {
		return icon
	}
	return "fas fa-globe"
}

// resolveGroupIconForName walks all namespaces to find the first whose group
// name matches the provided group, then returns its icon.
func resolveGroupIconForName(group string, nsMap map[string]namespaceAnnotations) string {
	for ns, ann := range nsMap {
		if namespaceGroupName(ns, ann) == group {
			return namespaceGroupIcon(ann)
		}
	}
	return "fas fa-globe"
}

// ---------------------------------------------------------------------------
// Template rendering
// ---------------------------------------------------------------------------

func (c *Controller) buildTemplateData(groups map[string][]ServiceItem) (string, error) {
	// Sort groups alphabetically (mirrors Jinja2's dictsort).
	groupNames := make([]string, 0, len(groups))
	for g := range groups {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)

	groupData := make([]GroupData, 0, len(groupNames))
	for _, gName := range groupNames {
		items := groups[gName]
		icon := ""
		if len(items) > 0 {
			icon = items[0].GroupIcon
		}
		gd := GroupData{
			Name: gName,
			Icon: icon,
		}
		for _, si := range items {
			gd.Items = append(gd.Items, si)
		}
		groupData = append(groupData, gd)
	}

	data := TemplateData{
		Title:    c.cfg.Title,
		Subtitle: c.cfg.Subtitle,
		Columns:  c.cfg.Columns,
		Groups:   groupData,
	}
	return renderConfig(data, c.cfg.TemplatePath)
}

// ---------------------------------------------------------------------------
// ConfigMap sync
// ---------------------------------------------------------------------------

func (c *Controller) syncConfigMap(ctx context.Context, rendered string) error {
	name := c.cfg.ConfigMapName
	ns := c.cfg.ConfigMapNamespace
	hash := contentHash(rendered)

	existing, err := c.clients.Core.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("get configmap %s/%s: %w", ns, name, err)
	}

	if errors.IsNotFound(err) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Data: map[string]string{"config.yml": rendered},
		}
		if _, err := c.clients.Core.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create configmap %s/%s: %w", ns, name, err)
		}
		slog.Info("created configmap", "namespace", ns, "name", name)
		return nil
	}

	// Skip update if content is unchanged.
	if contentHash(existing.Data["config.yml"]) == hash {
		slog.Debug("configmap already up to date", "namespace", ns, "name", name)
		return nil
	}

	existing.Data = map[string]string{"config.yml": rendered}
	if _, err := c.clients.Core.CoreV1().ConfigMaps(ns).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update configmap %s/%s: %w", ns, name, err)
	}
	slog.Info("updated configmap", "namespace", ns, "name", name)
	return nil
}

// ---------------------------------------------------------------------------
// Small utilities
// ---------------------------------------------------------------------------

func contentHash(s string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(s)))
}

func routeAnnotations(route map[string]interface{}) map[string]string {
	ann, _ := route["annotations"].(map[string]string)
	if ann == nil {
		return make(map[string]string)
	}
	return ann
}

func stringOr(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
