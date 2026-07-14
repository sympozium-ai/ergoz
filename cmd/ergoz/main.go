// ergoz is the CLI: install/uninstall the agent+collector into the current
// cluster and show fleet power status as a node → accelerator tree.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	"github.com/sympozium-ai/ergoz/internal/fleet"
	"github.com/sympozium-ai/ergoz/internal/installer"
)

// version is stamped via -ldflags at release time; `go install` builds fall
// back to the module version from build info.
var version = "dev"

func effectiveVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

func main() {
	var kubeconfig, kubecontext string

	root := &cobra.Command{
		Use:           "ergoz",
		Short:         "Ergoz — accelerator power telemetry for Kubernetes",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (default: standard loading rules)")
	root.PersistentFlags().StringVar(&kubecontext, "context", "", "Kubeconfig context to use")

	restCfg := func() (*rest.Config, error) {
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		if kubeconfig != "" {
			rules.ExplicitPath = kubeconfig
		}
		return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			rules, &clientcmd.ConfigOverrides{CurrentContext: kubecontext},
		).ClientConfig()
	}

	kflags := func() installer.KubeFlags {
		return installer.KubeFlags{Kubeconfig: kubeconfig, Context: kubecontext}
	}
	root.AddCommand(newStatusCmd(restCfg), newInstallCmd(restCfg, kflags), newUninstallCmd(restCfg, kflags), newVersionCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func newStatusCmd(restCfg func() (*rest.Config, error)) *cobra.Command {
	var output string
	var asJSON, asYAML bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show nodes, their accelerators, and current power draw",
		Long: `Show current fleet power draw as a node → accelerator tree.

Use -o json or -o yaml (or --json / --yaml) for machine-readable output —
the raw /api/v1/fleet snapshot, suitable for jq or scripting.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format := output
			switch {
			case asJSON && output == "tree":
				format = "json"
			case asYAML && output == "tree":
				format = "yaml"
			}
			if format != "tree" && format != "json" && format != "yaml" {
				return fmt.Errorf("invalid --output %q (want tree, json, or yaml)", format)
			}

			cfg, err := restCfg()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()

			body, err := installer.CollectorFleet(ctx, cfg)
			if err != nil {
				return err
			}
			var snap fleet.Snapshot
			if err := json.Unmarshal(body, &snap); err != nil {
				return fmt.Errorf("parsing fleet response: %w", err)
			}
			return renderStatus(cmd.OutOrStdout(), snap, format)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "tree", "Output format: tree, json, or yaml")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Shorthand for --output json")
	cmd.Flags().BoolVar(&asYAML, "yaml", false, "Shorthand for --output yaml")
	return cmd
}

// renderStatus writes the fleet snapshot in the requested format.
func renderStatus(w io.Writer, snap fleet.Snapshot, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(snap)
	case "yaml":
		b, err := yaml.Marshal(snap)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	default:
		printTree(w, snap)
		return nil
	}
}

func printTree(w io.Writer, snap fleet.Snapshot) {
	byNode := map[string][]fleet.DevicePower{}
	for _, d := range snap.Devices {
		byNode[d.Node] = append(byNode[d.Node], d)
	}
	nodes := make([]string, 0, len(byNode))
	for n := range byNode {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	fmt.Fprintf(w, "ERGOZ FLEET  ·  %d/%d agents up  ·  total %s  ·  scraped %s\n",
		snap.AgentsUp, snap.AgentsTotal, watts(snap.TotalWatts), snap.ScrapedAt.Local().Format("15:04:05"))

	if len(nodes) == 0 {
		fmt.Fprintln(w, "\n  (no accelerators reported — are agents running? kubectl -n ergoz-system get pods)")
		return
	}
	for ni, node := range nodes {
		nodeLast := ni == len(nodes)-1
		var nodeTotal float64
		for _, d := range byNode[node] {
			nodeTotal += d.PowerWatts
		}
		fmt.Fprintf(w, "%s %s  %s\n", branch(nodeLast), node, watts(nodeTotal))

		devs := byNode[node]
		for di, d := range devs {
			devLast := di == len(devs)-1
			fmt.Fprintf(w, "%s%s %s\n", indent(nodeLast), branch(devLast), deviceLine(d))
		}
	}
}

func deviceLine(d fleet.DevicePower) string {
	id := fmt.Sprintf("%-4s %-14s %s (%s:%s)", d.Kind, d.PCI, d.Driver, d.VendorID, d.DeviceID)
	if d.Suspended {
		return fmt.Sprintf("%s  suspended (0 W)", id)
	}
	suffix := ""
	if d.Stale {
		suffix = "  (stale)"
	}
	line := fmt.Sprintf("%s  %s%s", id, watts(d.PowerWatts), suffix)
	if len(d.Components) > 0 {
		parts := make([]string, 0, len(d.Components))
		for _, c := range []string{"socket", "gfx", "npu", "cpu_cores"} {
			if v, ok := d.Components[c]; ok {
				parts = append(parts, fmt.Sprintf("%s %s", c, watts(v)))
			}
		}
		if len(parts) > 0 {
			line += "  [" + strings.Join(parts, " · ") + "]"
		}
	}
	return line
}

func watts(w float64) string {
	return fmt.Sprintf("%.1f W", w)
}

func branch(last bool) string {
	if last {
		return "└─"
	}
	return "├─"
}

func indent(parentLast bool) string {
	if parentLast {
		return "   "
	}
	return "│  "
}

func newInstallCmd(restCfg func() (*rest.Config, error), kflags func() installer.KubeFlags) *cobra.Command {
	var imageTag, ghcrToken, ghcrUser string
	var wait time.Duration
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install Ergoz (agent DaemonSet + collector) into the current cluster",
		Long: `Installs Ergoz using the embedded Helm chart: the per-node agent
DaemonSet (reads accelerator power from host sysfs, read-only, non-root)
and the fleet collector. Creates a real Helm release ("ergoz" in
ergoz-system) — visible in 'helm list', upgradeable by re-running install.
No CRDs and no RBAC are created.

Use --image-tag to override the container image tag, for example when you
have sideloaded an image into Kind with a custom tag.

While the ghcr.io image package is private, pass --ghcr-token (a GitHub
token with read:packages) to create the pull secret automatically.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := restCfg()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()

			rev, fresh, err := installer.InstallOrUpgrade(ctx, kflags(), cfg, installer.InstallOptions{
				ImageTag:  imageTag,
				GHCRToken: ghcrToken,
				GHCRUser:  ghcrUser,
			})
			if err != nil {
				return err
			}
			if fresh {
				fmt.Printf("  ✅ Helm release %q installed (revision %d)\n", installer.ReleaseName, rev)
			} else {
				fmt.Printf("  ✅ Helm release %q upgraded (revision %d)\n", installer.ReleaseName, rev)
			}
			if wait > 0 {
				fmt.Println("  ⏳ waiting for components to become ready...")
				if err := installer.WaitReady(ctx, cfg, wait); err != nil {
					return err
				}
			}
			fmt.Println("\nErgoz installed. Try: ergoz status")
			return nil
		},
	}
	cmd.Flags().StringVar(&imageTag, "image-tag", "", "Override image tag (e.g. 'dev' for a kind-sideloaded build)")
	cmd.Flags().StringVar(&ghcrToken, "ghcr-token", "", "GitHub token (read:packages) — creates the image pull secret for the private ghcr package")
	cmd.Flags().StringVar(&ghcrUser, "ghcr-user", "", "Username for the ghcr pull secret (optional)")
	cmd.Flags().DurationVar(&wait, "wait", 90*time.Second, "Wait for readiness (0 to skip)")
	return cmd
}

func newUninstallCmd(restCfg func() (*rest.Config, error), kflags func() installer.KubeFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove Ergoz from the current cluster",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := restCfg()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			if err := installer.Uninstall(ctx, kflags(), cfg, 90*time.Second); err != nil {
				return err
			}
			fmt.Println("Ergoz uninstalled.")
			return nil
		},
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the ergoz CLI version",
		Run: func(*cobra.Command, []string) {
			fmt.Println("ergoz", effectiveVersion())
		},
	}
}
