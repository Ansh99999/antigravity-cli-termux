// Command agy-provider manages the custom providers the Antigravity CLI talks
// to, and runs the local proxy that translates for the ones it cannot talk to
// itself.
//
// The bootstrapper forwards `agy provider ...` here, so every subcommand below
// is reachable as `agy provider <name>`.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

var stdin = bufio.NewReader(os.Stdin)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage(os.Stdout)
		return
	}

	command, rest := args[0], args[1:]
	var err error
	switch command {
	case "list", "ls":
		err = cmdList(rest)
	case "status":
		err = cmdStatus(rest)
	case "add", "new":
		err = cmdAdd(rest)
	case "edit", "set":
		err = cmdEdit(rest)
	case "rm", "remove", "delete":
		err = cmdRemove(rest)
	case "use", "select":
		err = cmdUse(rest)
	case "key", "keys":
		err = cmdKey(rest)
	case "strategy", "rotation":
		err = cmdStrategy(rest)
	case "models", "discover":
		err = cmdModels(rest)
	case "test", "check":
		err = cmdTest(rest)
	case "up":
		err = cmdUp(rest)
	case "serve":
		err = cmdServe(rest)
	case "stop":
		err = cmdStop(rest)
	case "install-skill":
		err = cmdInstallSkill(rest)
	case "help", "-h", "--help":
		usage(os.Stdout)
	case "--version", "version":
		fmt.Println(buildVersion)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", command)
		usage(os.Stderr)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "%s %v\n", red("error:"), err)
		os.Exit(1)
	}
}

// buildVersion is stamped by build.sh.
var buildVersion = "dev"

func usage(w *os.File) {
	fmt.Fprint(w, `agy provider — custom endpoints, keys and rotation for the Antigravity CLI

  agy provider                       what is configured and what is active
  agy provider add                   add a provider, asking for each field
  agy provider add --name X --kind openai --base-url URL --key K [--model M]
  agy provider edit <name> [flags]   change one or more fields
  agy provider rm <name>             forget a provider and its keys
  agy provider use <name|none>       route the CLI here, or back to Google

  agy provider key add <name> [--key K --label L]
  agy provider key ls <name>
  agy provider key rm <name> <key-id>
  agy provider key on|off <name> <key-id>
  agy provider strategy <name> <first|rotate|random|least-errors>

  agy provider models <name> [--set M] [--map FROM=TO]
  agy provider test <name>           try every key against the endpoint
  agy provider status                keys, cooldowns and the proxy
  agy provider stop                  stop the translating proxy
  agy provider install-skill         add /provider to the CLI's slash menu

Styles: openai (any /chat/completions host), anthropic (/v1/messages),
gemini (generativelanguage). Keys never leave this device: they live in
`+configNote()+` and are sent only to the base URL you set.
`)
}

func configNote() string { return "$HOME/.config/agy/providers.json" }

// ---------- terminal helpers ----------

func colorize(code, s string) string {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return s
	}
	if fi, err := os.Stdout.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func bold(s string) string   { return colorize("1", s) }
func dim(s string) string    { return colorize("2", s) }
func red(s string) string    { return colorize("31", s) }
func green(s string) string  { return colorize("32", s) }
func yellow(s string) string { return colorize("33", s) }
func cyan(s string) string   { return colorize("36", s) }

// ask reads a line, showing a default in brackets.
func ask(prompt, fallback string) string {
	if fallback != "" {
		fmt.Printf("%s [%s]: ", prompt, dim(fallback))
	} else {
		fmt.Printf("%s: ", prompt)
	}
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return fallback
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return fallback
	}
	return line
}

// askYes reads a yes/no answer.
func askYes(prompt string, fallback bool) bool {
	hint := "y/N"
	if fallback {
		hint = "Y/n"
	}
	fmt.Printf("%s [%s]: ", prompt, dim(hint))
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return fallback
	case "y", "yes":
		return true
	}
	return false
}

// askSecret reads a line without echoing it, so a key is not left on screen or
// in the scrollback of a shared terminal.
func askSecret(prompt string) string {
	fmt.Printf("%s: ", prompt)
	restore, err := echoOff()
	if err == nil {
		defer restore()
	}
	line, _ := stdin.ReadString('\n')
	if err == nil {
		fmt.Println()
	}
	return strings.TrimSpace(line)
}

// mask shows enough of a key to recognise it and not enough to use it.
func mask(value string) string {
	if len(value) <= 8 {
		return strings.Repeat("•", len(value))
	}
	return value[:4] + strings.Repeat("•", 6) + value[len(value)-4:]
}
