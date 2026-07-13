// Package installer applies and removes the embedded ergoz manifests using
// server-side apply — the CLI equivalent of `kubectl apply -f deploy/`.
package installer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/yaml"
)

const fieldManager = "ergoz-cli"

// Namespace is where ergoz installs. Fixed for Phase 0.
const Namespace = "ergoz-system"

var imageTagRe = regexp.MustCompile(`(image:\s*ghcr\.io/sympozium-ai/ergoz):[\w.-]+`)

// Apply server-side-applies every document in the manifest. imageTag, when
// non-empty, overrides the image tag (e.g. a kind-sideloaded build).
// Returns the applied object identifiers in order.
func Apply(ctx context.Context, cfg *rest.Config, manifest []byte, imageTag string) ([]string, error) {
	if imageTag != "" {
		manifest = imageTagRe.ReplaceAll(manifest, []byte("${1}:"+imageTag))
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(disc))

	var applied []string
	dec := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	for {
		var raw map[string]interface{}
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			return applied, fmt.Errorf("decoding manifest: %w", err)
		}
		if len(raw) == 0 {
			continue
		}
		obj := &unstructured.Unstructured{Object: raw}

		gvk := obj.GroupVersionKind()
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return applied, fmt.Errorf("mapping %s: %w", gvk, err)
		}
		ri := resourceFor(dyn, mapping, obj)

		data, err := yaml.Marshal(obj.Object)
		if err != nil {
			return applied, err
		}
		if _, err := ri.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
			FieldManager: fieldManager,
			Force:        ptr(true),
		}); err != nil {
			return applied, fmt.Errorf("applying %s %s: %w", gvk.Kind, obj.GetName(), err)
		}
		applied = append(applied, fmt.Sprintf("%s/%s", gvk.Kind, obj.GetName()))
	}
	return applied, nil
}

func resourceFor(dyn dynamic.Interface, mapping *meta.RESTMapping, obj *unstructured.Unstructured) dynamic.ResourceInterface {
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := obj.GetNamespace()
		if ns == "" {
			ns = Namespace
		}
		return dyn.Resource(mapping.Resource).Namespace(ns)
	}
	return dyn.Resource(mapping.Resource)
}

// Uninstall deletes the ergoz namespace and waits for it to disappear.
func Uninstall(ctx context.Context, cfg *rest.Config, wait time.Duration) error {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	err = cs.CoreV1().Namespaces().Delete(ctx, Namespace, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("namespace %q not found — ergoz is not installed", Namespace)
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
func WaitReady(ctx context.Context, cfg *rest.Config, timeout time.Duration) error {
	cs, err := kubernetes.NewForConfig(cfg)
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
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("components not ready after %s — check: kubectl -n %s get pods", timeout, Namespace)
}

// CollectorFleet fetches the collector's /api/v1/fleet JSON through the
// kube-apiserver service proxy, so `ergoz status` needs no port-forward.
func CollectorFleet(ctx context.Context, cfg *rest.Config) ([]byte, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	body, err := cs.CoreV1().Services(Namespace).
		ProxyGet("http", "ergoz-collector", "9744", "/api/v1/fleet", nil).
		DoRaw(ctx)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("ergoz-collector service not found in %q — is ergoz installed? (ergoz install)", Namespace)
	}
	if err != nil {
		return nil, fmt.Errorf("querying collector: %w", err)
	}
	return body, nil
}

func ptr[T any](v T) *T { return &v }
