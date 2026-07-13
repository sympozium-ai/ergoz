// Package installer installs/uninstalls ergoz as a real Helm release from
// the embedded chart — `helm list` shows it, upgrades are Helm upgrades, and
// the same chart works standalone via `helm install charts/ergoz`.
package installer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/storage/driver"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/sympozium-ai/ergoz/charts"
)

// Namespace is where ergoz installs. Fixed for Phase 0.
const Namespace = "ergoz-system"

// ReleaseName is the Helm release name.
const ReleaseName = "ergoz"

// PullSecretName is the docker-registry secret created by --ghcr-token.
const PullSecretName = "ghcr-pull"

// KubeFlags carries the CLI's kubeconfig selection into Helm's config getter.
type KubeFlags struct {
	Kubeconfig string
	Context    string
}

func (k KubeFlags) getter(namespace string) *genericclioptions.ConfigFlags {
	cf := genericclioptions.NewConfigFlags(false)
	cf.Namespace = &namespace
	if k.Kubeconfig != "" {
		cf.KubeConfig = &k.Kubeconfig
	}
	if k.Context != "" {
		cf.Context = &k.Context
	}
	return cf
}

func newActionConfig(k KubeFlags) (*action.Configuration, error) {
	cfg := new(action.Configuration)
	if err := cfg.Init(k.getter(Namespace), Namespace, "secret", func(string, ...interface{}) {}); err != nil {
		return nil, fmt.Errorf("initializing helm: %w", err)
	}
	return cfg, nil
}

// LoadChart returns the embedded ergoz chart.
func LoadChart() (*chart.Chart, error) {
	var files []*loader.BufferedFile
	err := fs.WalkDir(charts.Ergoz, "ergoz", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(charts.Ergoz, path)
		if err != nil {
			return err
		}
		// Chart paths are relative to the chart root ("ergoz/Chart.yaml" → "Chart.yaml").
		files = append(files, &loader.BufferedFile{Name: path[len("ergoz/"):], Data: data})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading embedded chart: %w", err)
	}
	return loader.LoadFiles(files)
}

// InstallOptions parameterize InstallOrUpgrade.
type InstallOptions struct {
	// ImageTag overrides the image tag (e.g. a kind-sideloaded "dev" build).
	ImageTag string
	// GHCRToken, when set, creates/updates a docker-registry pull secret for
	// ghcr.io (needed while the image package is private) and wires it into
	// imagePullSecrets.
	GHCRToken string
	// GHCRUser is the username for the pull secret (any non-empty string
	// works for ghcr token auth; defaults to "token").
	GHCRUser string
	Timeout  time.Duration
}

// InstallOrUpgrade installs the embedded chart, or upgrades the existing
// release. Returns the release version and whether it was a fresh install.
func InstallOrUpgrade(ctx context.Context, k KubeFlags, restCfg *rest.Config, opts InstallOptions) (int, bool, error) {
	ch, err := LoadChart()
	if err != nil {
		return 0, false, err
	}

	vals := map[string]interface{}{}
	if opts.ImageTag != "" {
		vals["image"] = map[string]interface{}{"tag": opts.ImageTag}
	}
	if opts.GHCRToken != "" {
		if err := ensurePullSecret(ctx, restCfg, opts.GHCRUser, opts.GHCRToken); err != nil {
			return 0, false, err
		}
		vals["imagePullSecrets"] = []interface{}{map[string]interface{}{"name": PullSecretName}}
	}

	cfg, err := newActionConfig(k)
	if err != nil {
		return 0, false, err
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}

	hist := action.NewHistory(cfg)
	hist.Max = 1
	_, histErr := hist.Run(ReleaseName)
	switch {
	case errors.Is(histErr, driver.ErrReleaseNotFound):
		inst := action.NewInstall(cfg)
		inst.ReleaseName = ReleaseName
		inst.Namespace = Namespace
		inst.CreateNamespace = true
		inst.Timeout = timeout
		rel, err := inst.RunWithContext(ctx, ch, vals)
		if err != nil {
			return 0, false, fmt.Errorf("helm install: %w", err)
		}
		return rel.Version, true, nil
	case histErr != nil:
		// Not "release absent" — a real error (RBAC, API). Don't misroute
		// to upgrade, which would fail with a baffling "no deployed releases".
		return 0, false, fmt.Errorf("checking existing release: %w", histErr)
	}

	up := action.NewUpgrade(cfg)
	up.Namespace = Namespace
	up.Timeout = timeout
	// ReuseValues: a plain `ergoz install` re-run must not silently drop
	// imagePullSecrets/image.tag set on a prior install (which would break
	// image pulls). MaxHistory bounds accreting release Secrets.
	up.ReuseValues = true
	up.MaxHistory = 10
	rel, err := up.RunWithContext(ctx, ReleaseName, ch, vals)
	if err != nil {
		return 0, false, fmt.Errorf("helm upgrade: %w", err)
	}
	return rel.Version, false, nil
}

// ensurePullSecret creates or updates the ghcr docker-registry secret.
func ensurePullSecret(ctx context.Context, restCfg *rest.Config, user, token string) error {
	if user == "" {
		user = "token"
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return err
	}
	// The namespace may not exist yet on first install (Helm creates it
	// later) — create it here so the secret has a home.
	_, err = cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: Namespace},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	dockercfg := fmt.Sprintf(`{"auths":{"ghcr.io":{"username":%q,"password":%q}}}`, user, token)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: PullSecretName, Namespace: Namespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		StringData: map[string]string{corev1.DockerConfigJsonKey: dockercfg},
	}
	_, err = cs.CoreV1().Secrets(Namespace).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, err = cs.CoreV1().Secrets(Namespace).Update(ctx, secret, metav1.UpdateOptions{})
	}
	return err
}

// Uninstall removes the Helm release. Installs made by the pre-Helm CLI
// (raw server-side apply) have no release record, so when none is found it
// falls back to deleting the namespace.
func Uninstall(ctx context.Context, k KubeFlags, restCfg *rest.Config, wait time.Duration) error {
	cfg, err := newActionConfig(k)
	if err != nil {
		return err
	}
	hist := action.NewHistory(cfg)
	hist.Max = 1
	if _, err := hist.Run(ReleaseName); errors.Is(err, driver.ErrReleaseNotFound) {
		return legacyUninstall(ctx, restCfg, wait)
	}

	un := action.NewUninstall(cfg)
	un.Timeout = wait
	un.KeepHistory = false
	if _, err := un.Run(ReleaseName); err != nil {
		return fmt.Errorf("helm uninstall: %w", err)
	}
	// The chart's resources are gone; remove the (Helm-created) namespace too.
	return deleteNamespace(ctx, restCfg, wait)
}

func legacyUninstall(ctx context.Context, restCfg *rest.Config, wait time.Duration) error {
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return err
	}
	if _, err := cs.CoreV1().Namespaces().Get(ctx, Namespace, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		return fmt.Errorf("no Helm release %q and no namespace %q — ergoz is not installed", ReleaseName, Namespace)
	}
	return deleteNamespace(ctx, restCfg, wait)
}

func deleteNamespace(ctx context.Context, restCfg *rest.Config, wait time.Duration) error {
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return err
	}
	err = cs.CoreV1().Namespaces().Delete(ctx, Namespace, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if _, err := cs.CoreV1().Namespaces().Get(ctx, Namespace, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("namespace %q still terminating after %s (it will finish in the background)", Namespace, wait)
}

// WaitReady polls until the agent DaemonSet and collector Deployment report
// ready, or the timeout elapses.
func WaitReady(ctx context.Context, restCfg *rest.Config, timeout time.Duration) error {
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ds, dsErr := cs.AppsV1().DaemonSets(Namespace).Get(ctx, "ergoz-agent", metav1.GetOptions{})
		dep, depErr := cs.AppsV1().Deployments(Namespace).Get(ctx, "ergoz-collector", metav1.GetOptions{})
		if dsErr == nil && depErr == nil &&
			ds.Status.NumberReady == ds.Status.DesiredNumberScheduled && ds.Status.DesiredNumberScheduled > 0 &&
			dep.Status.ReadyReplicas == *dep.Spec.Replicas {
			return nil
		}
		// Fail fast with the actual reason rather than waiting out the full
		// timeout on an error that won't resolve itself (e.g. a private
		// image with no pull secret).
		if reason := blockingPodReason(ctx, cs); reason != "" {
			return fmt.Errorf("components not starting: %s", reason)
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("components not ready after %s — check: kubectl -n %s get pods", timeout, Namespace)
}

// blockingPodReason returns a human-readable cause when a pod is stuck in a
// state that won't self-heal (image pull, crash loop), or "" otherwise.
func blockingPodReason(ctx context.Context, cs kubernetes.Interface) string {
	pods, err := cs.CoreV1().Pods(Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return ""
	}
	for _, p := range pods.Items {
		for _, cs := range p.Status.ContainerStatuses {
			w := cs.State.Waiting
			if w == nil {
				continue
			}
			switch w.Reason {
			case "ImagePullBackOff", "ErrImagePull":
				hint := ""
				if strings.Contains(w.Message, "401") || strings.Contains(w.Message, "denied") {
					hint = "\n  The ergoz image is in a private registry. Re-run with:" +
						"\n    ergoz install --ghcr-token \"$(gh auth token)\"" +
						"\n  or make the ghcr package public."
				}
				return fmt.Sprintf("cannot pull the ergoz image (%s).%s", w.Reason, hint)
			case "CrashLoopBackOff":
				return fmt.Sprintf("pod %s is crash-looping (%s) — check: kubectl -n %s logs %s", p.Name, w.Reason, Namespace, p.Name)
			}
		}
	}
	return ""
}

// CollectorFleet fetches the collector's /api/v1/fleet JSON through the
// kube-apiserver service proxy, so `ergoz status` needs no port-forward.
func CollectorFleet(ctx context.Context, restCfg *rest.Config) ([]byte, error) {
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	// Address the port by its Service name ("http") so a chart-level port
	// override still resolves.
	body, err := cs.CoreV1().Services(Namespace).
		ProxyGet("http", "ergoz-collector", "http", "/api/v1/fleet", nil).
		DoRaw(ctx)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("ergoz-collector service not found in %q — is ergoz installed? (ergoz install)", Namespace)
	}
	if err != nil {
		return nil, fmt.Errorf("querying collector: %w", err)
	}
	return body, nil
}
