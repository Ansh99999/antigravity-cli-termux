package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/launch"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/proxy"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/state"
)

// cmdUp is what the bootstrapper runs before handing off to the engine. Its
// stdout is parsed, so only NAME=VALUE lines go there and everything human goes
// to stderr. It exits 0 even when it has nothing to offer: a broken provider
// registry must never stop the CLI from starting.
func cmdUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	noSettings := fs.Bool("no-settings", false, "do not touch the CLI's settings.json")
	quiet := fs.Bool("quiet", false, "say nothing on stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}

	env := launch.Up(!*noSettings)
	if !*quiet {
		for _, note := range env.Notes {
			fmt.Fprintf(os.Stderr, "[agy-provider] %s\n", note)
		}
		if env.Provider != nil {
			route := "direct"
			if env.ProxyPort > 0 {
				route = fmt.Sprintf("via 127.0.0.1:%d", env.ProxyPort)
			}
			fmt.Fprintf(os.Stderr, "[agy-provider] %s → %s (%s)\n", env.Provider.Name, env.Provider.BaseURL, route)
		}
	}
	for _, v := range env.Vars {
		fmt.Printf("%s=%s\n", v[0], v[1])
	}
	return nil
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	inherit := fs.Bool("inherit-fd", false, "serve on the listener passed as fd 3")
	host := fs.String("host", "127.0.0.1", "address to bind")
	port := fs.Int("port", 0, "port to bind; 0 asks the kernel")
	token := fs.String("token", os.Getenv("AGY_PROVIDER_TOKEN"), "the key the engine must present")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var ln net.Listener
	var err error
	if *inherit {
		// The launcher bound the port and handed it over, so the engine cannot
		// arrive before the socket is listening.
		file := os.NewFile(3, "listener")
		if file == nil {
			return fmt.Errorf("no listener on fd 3")
		}
		ln, err = net.FileListener(file)
		_ = file.Close()
	} else {
		ln, err = proxy.Listen(*host, *port)
	}
	if err != nil {
		return err
	}
	bound := ln.Addr().(*net.TCPAddr).Port

	store := state.NewStore()
	if err := state.Update(func(d *state.Data) {
		if d.Proxy == nil {
			d.Proxy = &state.Proxy{}
		}
		d.Proxy.PID = os.Getpid()
		d.Proxy.Port = bound
		if *token != "" {
			d.Proxy.Token = *token
		}
		if d.Proxy.StartedAt.IsZero() {
			d.Proxy.StartedAt = time.Now()
		}
	}); err != nil {
		return err
	}

	server := proxy.New(*token, store)
	done := make(chan struct{})
	go store.FlushEvery(2*time.Second, done)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-signals
		close(done)
		_ = ln.Close()
		_ = state.Update(func(d *state.Data) { d.Proxy = nil })
	}()

	fmt.Fprintf(os.Stderr, "[agy-provider] listening on 127.0.0.1:%d\n", bound)
	if err := server.Serve(ln); err != nil {
		select {
		case <-done:
			return nil // a closed listener is the ordinary way this ends
		default:
			return err
		}
	}
	return nil
}

func cmdStop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	stopped, err := launch.Stop()
	if err != nil {
		return err
	}
	if stopped {
		fmt.Printf("%s the translating proxy stopped.\n", green("✓"))
	} else {
		fmt.Println("No proxy was running.")
	}
	return nil
}

// skillPath is where the CLI looks for global skills.
func skillPath() string {
	if p := os.Getenv("AGY_CLI_SKILLS"); p != "" {
		return filepath.Join(p, "provider.md")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".gemini", "antigravity-cli", "skills", "provider.md")
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "skills", "provider.md")
}

func cmdInstallSkill(args []string) error {
	fs := flag.NewFlagSet("install-skill", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite an existing provider skill")
	if err := fs.Parse(args); err != nil {
		return err
	}

	path := skillPath()
	if _, err := os.Stat(path); err == nil && !*force {
		return fmt.Errorf("%s already exists; pass --force to replace it", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(skillMarkdown), 0o600); err != nil {
		return err
	}
	fmt.Printf("%s wrote %s\n", green("✓"), path)
	fmt.Println(dim("  Type /provider inside agy. Registered skills become slash commands,"))
	fmt.Println(dim("  so this is the closest thing to a built-in command a fork can add."))
	fmt.Println(dim("  It runs `agy provider …` as a tool call, which needs terminal permission."))
	return nil
}

// skillMarkdown is the skill the CLI turns into /provider.
const skillMarkdown = `---
name: provider
description: Inspect, add and switch the custom API providers this CLI sends requests to — base URL, multiple keys, rotation strategy and model choice — by running the agy provider command.
---

# provider

The user manages custom model providers with the ` + "`agy provider`" + ` command line
tool. It is already installed beside this CLI. Run the subcommand that matches
what was asked, then report what it printed in your own words.

## Reading

- ` + "`agy provider list`" + ` — every provider, which one is active
- ` + "`agy provider status`" + ` — the active provider, key health, cooldowns, the proxy
- ` + "`agy provider key ls <name>`" + ` — the keys of one provider, masked
- ` + "`agy provider models <name>`" + ` — ask the endpoint what it serves

## Changing

- ` + "`agy provider use <name>`" + ` or ` + "`agy provider use none`" + `
- ` + "`agy provider models <name> --set <model>`" + `
- ` + "`agy provider strategy <name> <first|rotate|random|least-errors>`" + `
- ` + "`agy provider edit <name> --base-url <url>`" + `

## Adding a provider or a key

These need a secret typed in, and a tool call is the wrong place for that: the
key would end up in this conversation's transcript. Do not ask the user to paste
a key to you, and never pass one on a command line. Tell them to run one of these
themselves, which asks without echoing:

- ` + "`agy provider add`" + `
- ` + "`agy provider key add <name>`" + `

## Notes

- Never print a key value, even if a command shows one.
- A provider served by the local translating proxy takes effect on the next
  request. One the engine talks to directly needs the CLI restarted; say so.
- ` + "`agy provider test <name>`" + ` checks every key against the endpoint without
  spending tokens. Add ` + "`--chat`" + ` to send one real short prompt.
`
