package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/config"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/discover"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/launch"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/settings"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/termuxnet"
)

// stringList collects a flag given more than once.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// defaultBase is the endpoint each style is usually pointed at, offered as the
// prompt's default so the common case is one keystroke.
func defaultBase(kind config.Kind) string {
	switch kind {
	case config.KindAnthropic:
		return "https://api.anthropic.com"
	case config.KindGemini:
		return "https://generativelanguage.googleapis.com"
	default:
		return "https://api.openai.com/v1"
	}
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	name := fs.String("name", "", "what to call this provider")
	kind := fs.String("kind", "", "openai, anthropic or gemini")
	baseURL := fs.String("base-url", "", "the endpoint root")
	model := fs.String("model", "", "the model to send every request to")
	strategy := fs.String("strategy", "", "first, rotate, random or least-errors")
	activate := fs.Bool("activate", true, "route the CLI here once it is added")
	noPrompt := fs.Bool("no-prompt", false, "never ask; fail if a field is missing")
	var keyFlags, headerFlags stringList
	fs.Var(&keyFlags, "key", "an API key; repeat for several")
	fs.Var(&headerFlags, "header", "an extra header as Name=Value; repeat for several")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	interactive := !*noPrompt && isTerminal()
	p := &config.Provider{Name: *name}
	if p.Name == "" {
		if !interactive {
			return fmt.Errorf("--name is required")
		}
		p.Name = ask("Name for this provider", "")
	}
	if _, err := cfg.Find(p.Name); err == nil {
		return fmt.Errorf("provider %q already exists; use `agy provider edit %s`", p.Name, p.Name)
	}

	styleText := *kind
	if styleText == "" {
		if !interactive {
			return fmt.Errorf("--kind is required")
		}
		fmt.Println(dim("  openai     any host that answers POST /chat/completions"))
		fmt.Println(dim("  anthropic  a host that answers POST /v1/messages"))
		fmt.Println(dim("  gemini     a generativelanguage-shaped host"))
		styleText = ask("Style", "openai")
	}
	parsedKind, err := config.ParseKind(styleText)
	if err != nil {
		return err
	}
	p.Kind = parsedKind

	p.BaseURL = *baseURL
	if p.BaseURL == "" {
		if !interactive {
			return fmt.Errorf("--base-url is required")
		}
		p.BaseURL = ask("Base URL", defaultBase(p.Kind))
	}

	for _, k := range keyFlags {
		p.Keys = append(p.Keys, config.Key{ID: nextKeyID(p), Value: strings.TrimSpace(k)})
	}
	if len(p.Keys) == 0 && interactive {
		for {
			value := askSecret(fmt.Sprintf("API key %d (blank when done)", len(p.Keys)+1))
			if value == "" {
				break
			}
			p.Keys = append(p.Keys, config.Key{ID: nextKeyID(p), Value: value})
		}
	}
	if len(p.Keys) == 0 {
		return fmt.Errorf("a provider needs at least one key (--key)")
	}

	if err := applyHeaders(p, headerFlags); err != nil {
		return err
	}

	if *strategy != "" {
		s, err := config.ParseStrategy(*strategy)
		if err != nil {
			return err
		}
		p.Strategy = s
	} else if len(p.Keys) > 1 && interactive {
		s, err := config.ParseStrategy(ask("Key strategy (first, rotate, random, least-errors)", "rotate"))
		if err != nil {
			return err
		}
		p.Strategy = s
	}

	p.Model = *model
	if err := p.Validate(); err != nil {
		return err
	}

	if p.Model == "" && interactive {
		if chosen := chooseModel(p); chosen != "" {
			p.Model = chosen
		}
	}

	cfg.Upsert(p)
	if *activate {
		cfg.Active = p.Name
	}
	if err := config.Save(cfg); err != nil {
		return err
	}

	fmt.Printf("\n%s %s %s\n", green("✓"), bold(p.Name), dim("saved to "+config.Path()))
	if *activate {
		return activated(p)
	}
	fmt.Println(dim("Not active yet: ") + bold("agy provider use "+p.Name))
	return nil
}

// activated tells the user what will happen on the next request, which differs
// by whether the engine can reach the host itself.
func activated(p *config.Provider) error {
	if _, err := launch.ActivateSettings(); err != nil {
		fmt.Fprintf(os.Stderr, "%s could not set %s in %s: %v\n", yellow("warning:"), settings.Key, settings.Path(), err)
	}
	fmt.Printf("%s the CLI now uses %s.\n", green("✓"), bold(p.Name))
	if p.NeedsProxy() {
		fmt.Println(dim("  A local translating proxy serves it; it starts with the next `agy`"))
		fmt.Println(dim("  and picks up later changes without a restart."))
	} else {
		fmt.Println(dim("  The engine talks to it directly. Restart `agy` to pick up the change."))
	}
	return nil
}

func cmdEdit(args []string) error {
	fs := flag.NewFlagSet("edit", flag.ContinueOnError)
	kind := fs.String("kind", "", "openai, anthropic or gemini")
	baseURL := fs.String("base-url", "", "the endpoint root")
	model := fs.String("model", "", "the model to send every request to")
	rename := fs.String("name", "", "a new name")
	strategy := fs.String("strategy", "", "first, rotate, random or least-errors")
	cooldown := fs.Int("cooldown", 0, "seconds a rejected key rests")
	attempts := fs.Int("attempts", 0, "how many keys one request may try")
	clearHeaders := fs.Bool("clear-headers", false, "drop every extra header")
	var headerFlags stringList
	fs.Var(&headerFlags, "header", "an extra header as Name=Value; repeat for several")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: agy provider edit <name> [flags]")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	p, err := cfg.Find(fs.Arg(0))
	if err != nil {
		return err
	}
	wasActive := strings.EqualFold(cfg.Active, p.Name)

	if *kind != "" {
		parsed, err := config.ParseKind(*kind)
		if err != nil {
			return err
		}
		p.Kind = parsed
	}
	if *baseURL != "" {
		p.BaseURL = *baseURL
	}
	if *model != "" {
		p.Model = strings.TrimSpace(*model)
		if p.Model == "-" {
			p.Model = ""
		}
	}
	if *strategy != "" {
		parsed, err := config.ParseStrategy(*strategy)
		if err != nil {
			return err
		}
		p.Strategy = parsed
	}
	if *cooldown > 0 {
		p.CooldownSeconds = *cooldown
	}
	if *attempts > 0 {
		p.MaxAttempts = *attempts
	}
	if *clearHeaders {
		p.Headers = nil
	}
	if err := applyHeaders(p, headerFlags); err != nil {
		return err
	}
	if *rename != "" {
		p.Name = *rename
		if wasActive {
			cfg.Active = p.Name
		}
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("%s %s updated.\n", green("✓"), bold(p.Name))
	return nil
}

func applyHeaders(p *config.Provider, flags stringList) error {
	for _, h := range flags {
		name, value, found := strings.Cut(h, "=")
		if !found {
			return fmt.Errorf("--header wants Name=Value, got %q", h)
		}
		name = strings.TrimSpace(name)
		replaced := false
		for i := range p.Headers {
			if strings.EqualFold(p.Headers[i].Name, name) {
				p.Headers[i].Value = value
				replaced = true
				break
			}
		}
		if !replaced {
			p.Headers = append(p.Headers, config.Header{Name: name, Value: value})
		}
	}
	return nil
}

func cmdRemove(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: agy provider rm <name>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	p, err := cfg.Find(args[0])
	if err != nil {
		return err
	}
	if isTerminal() && !askYes(fmt.Sprintf("Forget %s and its %s?", bold(p.Name), plural(len(p.Keys), "key", "keys")), false) {
		fmt.Println("Left alone.")
		return nil
	}
	wasActive := strings.EqualFold(cfg.Active, p.Name)
	if err := cfg.Remove(p.Name); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("%s %s removed.\n", green("✓"), p.Name)
	if wasActive {
		return deactivate()
	}
	return nil
}

func cmdUse(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: agy provider use <name|none>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if strings.EqualFold(args[0], "none") || args[0] == "-" {
		cfg.Active = ""
		if err := config.Save(cfg); err != nil {
			return err
		}
		return deactivate()
	}

	p, err := cfg.Find(args[0])
	if err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	cfg.Active = p.Name
	if err := config.Save(cfg); err != nil {
		return err
	}
	return activated(p)
}

// deactivate hands the CLI back to its own sign-in and stops the proxy, since
// nothing is left for it to serve.
func deactivate() error {
	if stopped, err := launch.Stop(); err == nil && stopped {
		fmt.Println(dim("Stopped the translating proxy."))
	}
	restored, err := launch.RestoreSettings()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s could not restore %s: %v\n", yellow("warning:"), settings.Key, err)
	}
	if restored == "" {
		fmt.Printf("%s back to the CLI's own sign-in. Restart `agy` to pick up the change.\n", green("✓"))
	} else {
		fmt.Printf("%s %s restored to %q. Restart `agy` to pick up the change.\n", green("✓"), settings.Key, restored)
	}
	return nil
}

func nextKeyID(p *config.Provider) string {
	for n := len(p.Keys) + 1; ; n++ {
		candidate := fmt.Sprintf("k%d", n)
		taken := false
		for _, k := range p.Keys {
			if k.ID == candidate {
				taken = true
				break
			}
		}
		if !taken {
			return candidate
		}
	}
}

// chooseModel offers what the host says it serves. A host that will not answer
// its listing endpoint is common enough that this is never fatal.
func chooseModel(p *config.Provider) string {
	fmt.Print(dim("Asking " + p.ModelsURL() + " what it serves… "))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	models, err := discover.Models(ctx, termuxnet.Client(), p, p.EnabledKeys()[0].Value)
	if err != nil {
		fmt.Println(yellow("no list: ") + truncate(err.Error(), 80))
		return ask("Model to use (blank to decide later)", "")
	}
	fmt.Printf("%s\n", green(fmt.Sprintf("%d found", len(models))))
	p.Discovered = models
	return pickFromList(models, "Model")
}

func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
