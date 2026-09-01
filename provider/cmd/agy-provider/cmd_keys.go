package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/config"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/state"
)

func cmdKey(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agy provider key <add|ls|rm|on|off> <provider> [...]")
	}
	action, rest := args[0], args[1:]
	switch action {
	case "add":
		return keyAdd(rest)
	case "ls", "list":
		return keyList(rest)
	case "rm", "remove", "delete":
		return keySet(rest, "rm")
	case "on", "enable":
		return keySet(rest, "on")
	case "off", "disable":
		return keySet(rest, "off")
	}
	return fmt.Errorf("unknown key action %q", action)
}

// resolve finds the provider a key command is about, defaulting to the active
// one so `agy provider key add` alone does the obvious thing.
func resolve(cfg *config.File, name string) (*config.Provider, error) {
	if name == "" {
		if cfg.Active == "" {
			return nil, fmt.Errorf("name a provider (nothing is active)")
		}
		name = cfg.Active
	}
	return cfg.Find(name)
}

func keyAdd(args []string) error {
	fs := flag.NewFlagSet("key add", flag.ContinueOnError)
	value := fs.String("key", "", "the API key; omitted means ask without echoing")
	label := fs.String("label", "", "a note about whose key this is")
	strategy := fs.String("strategy", "", "set the rotation strategy at the same time")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	p, err := resolve(cfg, fs.Arg(0))
	if err != nil {
		return err
	}

	added := 0
	if *value != "" {
		p.Keys = append(p.Keys, config.Key{ID: nextKeyID(p), Value: strings.TrimSpace(*value), Label: *label})
		added++
	} else {
		if !isTerminal() {
			return fmt.Errorf("--key is required when there is no terminal to ask at")
		}
		for {
			entered := askSecret(fmt.Sprintf("API key %d (blank when done)", len(p.Keys)+1))
			if entered == "" {
				break
			}
			p.Keys = append(p.Keys, config.Key{ID: nextKeyID(p), Value: entered, Label: *label})
			added++
		}
	}
	if added == 0 {
		fmt.Println("Nothing added.")
		return nil
	}

	if *strategy != "" {
		parsed, err := config.ParseStrategy(*strategy)
		if err != nil {
			return err
		}
		p.Strategy = parsed
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("%s %s now has %s, %s.\n", green("✓"), bold(p.Name),
		plural(len(p.EnabledKeys()), "enabled key", "enabled keys"), p.EffectiveStrategy())
	if p.Kind == config.KindGemini && len(p.EnabledKeys()) > 1 {
		fmt.Println(dim("  Rotating several keys needs the local proxy, so it now serves this provider."))
	}
	return nil
}

func keyList(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	p, err := resolve(cfg, name)
	if err != nil {
		return err
	}
	if len(p.Keys) == 0 {
		fmt.Printf("%s has no keys.\n", p.Name)
		return nil
	}
	data := state.Load()
	fmt.Printf("%s %s\n", bold(p.Name), dim(string(p.EffectiveStrategy())))
	for _, k := range p.Keys {
		h := data.Health(p.Name, k.ID)
		flags := []string{}
		if !k.On() {
			flags = append(flags, yellow("off"))
		}
		if h.Resting(time.Now()) {
			flags = append(flags, red("resting"))
		}
		if k.Label != "" {
			flags = append(flags, dim(k.Label))
		}
		fmt.Printf("  %-6s %-20s %s\n", k.ID, mask(k.Value), strings.Join(flags, " "))
	}
	return nil
}

func keySet(args []string, action string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: agy provider key %s <provider> <key-id>", action)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name, keyID := "", args[0]
	if len(args) > 1 {
		name, keyID = args[0], args[1]
	}
	p, err := resolve(cfg, name)
	if err != nil {
		return err
	}

	for i := range p.Keys {
		if !strings.EqualFold(p.Keys[i].ID, keyID) {
			continue
		}
		switch action {
		case "rm":
			p.Keys = append(p.Keys[:i], p.Keys[i+1:]...)
			fmt.Printf("%s %s removed from %s.\n", green("✓"), keyID, p.Name)
		case "on":
			on := true
			p.Keys[i].Enabled = &on
			fmt.Printf("%s %s enabled.\n", green("✓"), keyID)
		case "off":
			off := false
			p.Keys[i].Enabled = &off
			fmt.Printf("%s %s disabled.\n", green("✓"), keyID)
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		// A removed or disabled key should not keep a cooldown that outlives it.
		return state.Update(func(d *state.Data) {
			delete(d.Keys, p.Name+"/"+keyID)
		})
	}
	return fmt.Errorf("%s has no key %q", p.Name, keyID)
}

func cmdStrategy(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agy provider strategy <name> <first|rotate|random|least-errors>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	name, want := "", args[0]
	if len(args) > 1 {
		name, want = args[0], args[1]
	}
	p, err := resolve(cfg, name)
	if err != nil {
		return err
	}
	parsed, err := config.ParseStrategy(want)
	if err != nil {
		return err
	}
	p.Strategy = parsed
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("%s %s picks keys %s.\n", green("✓"), bold(p.Name), bold(string(parsed)))
	switch parsed {
	case config.StrategyFirst:
		fmt.Println(dim("  The first key is used until it fails; the rest are failover."))
	case config.StrategyRotate:
		fmt.Println(dim("  Each request takes the next key in order."))
	case config.StrategyRandom:
		fmt.Println(dim("  Each request takes a key at random."))
	case config.StrategyLeastErrors:
		fmt.Println(dim("  The key with the fewest recent failures goes first."))
	}
	return nil
}
