package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/CYBERSZP0NT/szmelc-router/internal/config"
	"github.com/CYBERSZP0NT/szmelc-router/pkg/router"
)

var (
	routeRole       string
	routeComplexity int
	routeTools      bool
	routeLocal      bool
	routeCatalog    bool
	routeAll        bool
	routeMode       string
)

var routeCmd = &cobra.Command{
	Use:   "route [prompt...]",
	Short: "Show what the agentic task router would pick for a prompt (no upstream call)",
	Long: `The task router turns model "agent" (or "task") into a per-request pick:
it classifies the prompt (role + complexity 1-5) and builds a chain. In the
default mode, [Rozkład Jazdy], that is all in: the strongest available model
first, whatever the task. In Jako Tako it is the tier that is just enough:
cheapest sufficient model first, stronger ones next, weaker ones last. Tiers, costs and rules are data
(defaults/agentic.yaml + the agentic: section of your config).

This command previews the decision without calling any provider. The prompt
comes from the arguments, or from stdin when there are none.

Force the classification with --role/--complexity, or in a request with
model "agent:code:4", the X-Freellm-Task header or metadata.task.

Examples:
  szmelc-router route "hi"
  szmelc-router route "refactor this Go service for concurrency" --all
  echo "summarize: ..." | szmelc-router route
  szmelc-router route --role code --complexity 5 --local "x"
  szmelc-router route --mode jako-tako "write a haiku"
  szmelc-router route --catalog
  szmelc-router chat --model agent "explain CRDTs"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(pCfgPath)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		r, err := buildRouter(cfg, globalTimeout())
		if err != nil {
			return fmt.Errorf("router: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), globalTimeout())
		defer cancel()

		if routeCatalog {
			return printTaskCatalog(ctx, r)
		}

		prompt := strings.Join(args, " ")
		if prompt == "" {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			prompt = strings.TrimSpace(string(b))
		}
		if prompt == "" {
			return fmt.Errorf("no prompt: pass it as arguments or on stdin")
		}

		var over []string
		if routeRole != "" {
			over = append(over, "role="+routeRole)
		}
		if routeComplexity > 0 {
			over = append(over, "complexity="+strconv.Itoa(routeComplexity))
		}
		if routeLocal {
			over = append(over, "local")
		}
		d, err := r.PlanTask(ctx, "", []router.Message{{Role: "user", Content: prompt}}, routeTools,
			&router.TaskHints{Override: strings.Join(over, ","), Mode: routeMode})
		if err != nil {
			return err
		}
		if pJSON {
			printJSON(d)
			return nil
		}

		fmt.Printf("mode     %s\n", d.ModeName)
		fmt.Printf("need     %s, complexity %d (%s", d.Need.Role, d.Need.Complexity, d.Need.Source)
		if len(d.Need.Signals) > 0 {
			fmt.Printf(": %s", strings.Join(d.Need.Signals, " "))
		}
		fmt.Printf("), ~%d tokens\n", d.Need.Tokens)
		fmt.Printf("tier     %d\n", d.Required)
		if d.Winner != nil {
			fmt.Printf("pick     %s  (%s)\n", d.Winner.Model, d.Winner.ID)
		}
		fmt.Printf("why      %s\n\n", d.Reason)

		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "#\tID\tMODEL\tTIER\tCOST\tKIND\tWHY NOT FIRST")
		cands := append([]router.TaskCandidate(nil), d.Cands...)
		sort.SliceStable(cands, func(i, j int) bool {
			ri, rj := cands[i].Rank, cands[j].Rank
			return ri != 0 && (rj == 0 || ri < rj)
		})
		for _, c := range cands {
			if c.Rank == 0 && !routeAll {
				continue
			}
			rank := "-"
			if c.Rank > 0 {
				rank = strconv.Itoa(c.Rank)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n", rank, c.ID, c.Model, c.Tier, c.Cost, c.Where, c.Lost)
		}
		tw.Flush()
		if !routeAll {
			fmt.Println("\n(--all also lists entries left out of the chain and why)")
		}
		return nil
	},
}

func printTaskCatalog(ctx context.Context, r *router.Router) error {
	spec := r.TaskSpec()
	avail := r.TaskAvailability(ctx)
	if pJSON {
		printJSON(map[string]any{"aliases": r.TaskAliases(), "roles": spec.Roles, "complexity": spec.Complexity,
			"rules": spec.Rules, "catalog": spec.Catalog, "available": avail})
		return nil
	}
	fmt.Printf("mode     %s (%s)\n", router.TaskModes()[modeIndex(r.TaskMode())].Name, r.TaskMode())
	fmt.Printf("aliases  %s   (enabled: %v, escalations: %d)\n", strings.Join(r.TaskAliases(), ", "), spec.IsEnabled(), spec.MaxEscalations())
	fmt.Print("roles   ")
	for _, role := range spec.RolesByTier() {
		fmt.Printf(" %s=%d", role, spec.Roles[role])
	}
	fmt.Println()
	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tMODEL\tTIER\tCOST\tSPEED\tTOOLS\tKIND\tGOOD AT\tAVAILABLE")
	for _, e := range spec.Catalog {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%v\t%s\t%s\t%s\n", e.ID, e.Model, e.Tier, e.Cost, e.Speed, e.Tools, e.Where,
			strings.Join(e.GoodAt, ","), avail[e.ID])
	}
	tw.Flush()
	return nil
}

func modeIndex(id string) int {
	for i, m := range router.TaskModes() {
		if m.ID == id {
			return i
		}
	}
	return 0
}

func init() {
	routeCmd.Flags().StringVar(&routeMode, "mode", "", "mode for this preview: rozklad-jazdy ([Rozkład Jazdy], default) | jako-tako (Jako Tako)")
	routeCmd.Flags().StringVar(&routeRole, "role", "", "force the role (classify|extract|chat|summarize|translate|write|research|review|plan|code|reason)")
	routeCmd.Flags().IntVar(&routeComplexity, "complexity", 0, "force the complexity (1-5)")
	routeCmd.Flags().BoolVar(&routeTools, "tools", false, "the request carries tool definitions (only tool-capable models qualify)")
	routeCmd.Flags().BoolVar(&routeLocal, "local", false, "keep it on this machine (local providers only)")
	routeCmd.Flags().BoolVar(&routeCatalog, "catalog", false, "print the task-router catalog with live availability instead")
	routeCmd.Flags().BoolVar(&routeAll, "all", false, "also list catalog entries that are not in the chain")
	rootCmd.AddCommand(routeCmd)
}
