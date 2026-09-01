package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/config"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/settings"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/state"
)

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.Providers) == 0 {
		fmt.Println("No providers configured. Add one with " + bold("agy provider add") + ".")
		fmt.Println(dim("Until then the CLI signs in to Google as usual."))
		return nil
	}

	fmt.Println(bold("Providers") + dim("  (● active)"))
	for _, p := range cfg.Providers {
		marker := "  "
		name := p.Name
		if strings.EqualFold(cfg.Active, p.Name) {
			marker = green("● ")
			name = bold(p.Name)
		}
		fmt.Printf("%s%-22s %-10s %s\n", marker, name, cyan(string(p.Kind)), dim(p.BaseURL))

		detail := fmt.Sprintf("%s, %s", plural(len(p.EnabledKeys()), "key", "keys"), p.EffectiveStrategy())
		if total := len(p.Keys); total != len(p.EnabledKeys()) {
			detail += fmt.Sprintf(" (%d disabled)", total-len(p.EnabledKeys()))
		}
		if p.Model != "" {
			detail += " · model " + p.Model
		}
		if len(p.ModelMap) > 0 {
			detail += fmt.Sprintf(" · %s mapped", plural(len(p.ModelMap), "model", "models"))
		}
		route := "direct"
		if p.NeedsProxy() {
			route = "via local proxy"
		}
		fmt.Printf("    %s %s\n", dim(detail), dim("· "+route))
	}

	if cfg.Active == "" {
		fmt.Println("\n" + dim("Nothing is active; the CLI uses its own Google sign-in. ") + bold("agy provider use <name>"))
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	data := state.Load()

	active := cfg.ActiveProvider()
	if active == nil {
		fmt.Println(bold("Active:") + " " + yellow("none") + dim(" — the CLI signs in to Google"))
	} else {
		fmt.Printf("%s %s %s\n", bold("Active:"), active.Name, dim("("+string(active.Kind)+" · "+active.BaseURL+")"))
		if active.Model != "" {
			fmt.Printf("%s %s\n", bold("Model: "), active.Model)
		}
		fmt.Printf("%s %s\n", bold("Keys:  "), fmt.Sprintf("%s, %s", plural(len(active.EnabledKeys()), "enabled key", "enabled keys"), active.EffectiveStrategy()))
	}

	if current := settings.Current(); current != "" {
		fmt.Printf("%s %s %s\n", bold("Setting:"), settings.Key+" = "+current, dim(settings.Path()))
	} else {
		fmt.Printf("%s %s\n", bold("Setting:"), dim(settings.Key+" is unset in "+settings.Path()))
	}

	if p := data.Proxy; p.Live() {
		fmt.Printf("%s %s\n", bold("Proxy: "), fmt.Sprintf("%s on 127.0.0.1:%d, pid %d, up %s",
			green("running"), p.Port, p.PID, time.Since(p.StartedAt).Round(time.Second)))
	} else if active != nil && active.NeedsProxy() {
		fmt.Printf("%s %s\n", bold("Proxy: "), dim("not running — it starts with the next `agy`"))
	}

	if active == nil {
		return nil
	}
	fmt.Println()
	fmt.Println(bold("Key health"))
	for _, k := range active.Keys {
		h := data.Health(active.Name, k.ID)
		label := k.ID
		if k.Label != "" {
			label += " " + dim("("+k.Label+")")
		}
		var notes []string
		if !k.On() {
			notes = append(notes, yellow("disabled"))
		}
		if h.Resting(time.Now()) {
			notes = append(notes, red("resting "+time.Until(h.CooldownUntil).Round(time.Second).String()))
		}
		if h.Uses > 0 {
			notes = append(notes, fmt.Sprintf("%d used", h.Uses))
		}
		if h.Errors > 0 {
			notes = append(notes, fmt.Sprintf("%d failed", h.Errors))
		}
		if len(notes) == 0 {
			notes = append(notes, green("ready"))
		}
		fmt.Printf("  %-28s %s %s\n", label, dim(mask(k.Value)), strings.Join(notes, dim(" · ")))
		if h.LastError != "" {
			fmt.Printf("      %s\n", dim("last: "+truncate(h.LastError, 100)))
		}
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
