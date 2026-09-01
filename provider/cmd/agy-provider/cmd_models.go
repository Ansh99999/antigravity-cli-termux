package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/config"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/discover"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/termuxnet"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/wire"
)

func cmdModels(args []string) error {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	set := fs.String("set", "", "pin this model for every request")
	mapping := fs.String("map", "", "rewrite one model into another, as ASKED=SENT")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name := fs.Arg(0)
	if name == "" {
		if cfg.Active == "" {
			return fmt.Errorf("usage: agy provider models <name>")
		}
		name = cfg.Active
	}
	p, err := cfg.Find(name)
	if err != nil {
		return err
	}

	if *mapping != "" {
		asked, sent, found := strings.Cut(*mapping, "=")
		if !found {
			return fmt.Errorf("--map wants ASKED=SENT, got %q", *mapping)
		}
		if p.ModelMap == nil {
			p.ModelMap = map[string]string{}
		}
		if sent == "" {
			delete(p.ModelMap, asked)
		} else {
			p.ModelMap[asked] = sent
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("%s %s → %s\n", green("✓"), asked, sent)
		return nil
	}

	if *set != "" {
		p.Model = *set
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("%s %s sends every request to %s.\n", green("✓"), p.Name, bold(p.Model))
		return nil
	}

	if len(p.EnabledKeys()) == 0 {
		return fmt.Errorf("provider %q has no enabled key to ask with", p.Name)
	}
	fmt.Print(dim("Asking " + p.ModelsURL() + "… "))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	models, err := discover.Models(ctx, termuxnet.Client(), p, p.EnabledKeys()[0].Value)
	if err != nil {
		fmt.Println()
		return err
	}
	fmt.Printf("%s\n\n", green(fmt.Sprintf("%d models", len(models))))
	p.Discovered = models
	if err := config.Save(cfg); err != nil {
		return err
	}

	if !isTerminal() {
		for _, m := range models {
			fmt.Println(m)
		}
		return nil
	}
	chosen := pickFromList(models, "Pin a model (blank to leave as is)")
	if chosen == "" || chosen == p.Model {
		return nil
	}
	p.Model = chosen
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("%s %s sends every request to %s.\n", green("✓"), p.Name, bold(p.Model))
	return nil
}

// pickFromList shows a numbered list, narrows it as the user types part of a
// name, and accepts a name it never listed — a gateway's listing is not always
// the whole truth about what it will serve.
func pickFromList(items []string, label string) string {
	if len(items) == 0 {
		return ask(label, "")
	}
	view := items
	for {
		show := view
		if len(show) > 30 {
			show = show[:30]
		}
		for i, m := range show {
			fmt.Printf("  %2d  %s\n", i+1, m)
		}
		if len(view) > len(show) {
			fmt.Println(dim(fmt.Sprintf("  … and %d more — type part of a name to narrow it", len(view)-len(show))))
		}
		answer := ask(label, "")
		if answer == "" {
			return ""
		}
		if n, err := strconv.Atoi(answer); err == nil {
			if n >= 1 && n <= len(show) {
				return show[n-1]
			}
			fmt.Println(dim("  no such number"))
			continue
		}
		narrowed := filterContains(items, answer)
		switch len(narrowed) {
		case 1:
			return narrowed[0]
		case 0:
			return answer
		default:
			view = narrowed
		}
	}
}

func filterContains(items []string, needle string) []string {
	needle = strings.ToLower(needle)
	var out []string
	for _, item := range items {
		if strings.Contains(strings.ToLower(item), needle) {
			out = append(out, item)
		}
	}
	return out
}

func cmdTest(args []string) error {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	chat := fs.Bool("chat", false, "also send a one-word prompt through the full translation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name := fs.Arg(0)
	if name == "" {
		name = cfg.Active
	}
	if name == "" {
		return fmt.Errorf("usage: agy provider test <name>")
	}
	p, err := cfg.Find(name)
	if err != nil {
		return err
	}
	if len(p.EnabledKeys()) == 0 {
		return fmt.Errorf("provider %q has no enabled key", p.Name)
	}

	fmt.Printf("%s %s\n\n", bold(p.Name), dim(string(p.Kind)+" · "+p.BaseURL))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := termuxnet.Client()
	failures := 0
	for _, r := range discover.TestKeys(ctx, client, p) {
		label := r.KeyID
		if r.Label != "" {
			label += " (" + r.Label + ")"
		}
		if r.OK {
			fmt.Printf("  %s %-24s %s\n", green("✓"), label,
				dim(fmt.Sprintf("%d models, %s", r.Models, r.Latency.Round(time.Millisecond))))
			continue
		}
		failures++
		detail := r.Detail
		if r.Status > 0 {
			detail = fmt.Sprintf("HTTP %d — %s", r.Status, detail)
		}
		fmt.Printf("  %s %-24s %s\n", red("✗"), label, truncate(detail, 100))
	}

	if *chat {
		fmt.Println()
		if err := probeChat(ctx, client, p); err != nil {
			failures++
			fmt.Printf("  %s %s\n", red("✗"), truncate(err.Error(), 200))
		}
	}
	if failures > 0 {
		return fmt.Errorf("%s", plural(failures, "check failed", "checks failed"))
	}
	return nil
}

// probeChat sends the smallest real request through the same translation the
// proxy uses, which is the only check that proves the whole path end to end.
func probeChat(ctx context.Context, client *http.Client, p *config.Provider) error {
	model := p.ResolveModel("gemini-3-pro")
	if model == "" {
		return fmt.Errorf("no model to test with; set one with `agy provider models %s`", p.Name)
	}
	limit := 16
	g := &wire.GeminiRequest{
		Contents: []wire.GeminiContent{{
			Role:  wire.RoleUser,
			Parts: []wire.GeminiPart{{Text: "Reply with the single word: ready"}},
		}},
		GenerationConfig: &wire.GeminiGenConfig{MaxOutputTokens: &limit},
	}

	var url string
	var body []byte
	var err error
	decode := wire.FromOpenAI
	switch p.Kind {
	case config.KindAnthropic:
		req, buildErr := wire.ToAnthropic(g, model, false)
		if buildErr != nil {
			return buildErr
		}
		url = p.ChatURL()
		decode = wire.FromAnthropic
		body, err = json.Marshal(req)
	case config.KindGemini:
		url = p.GeminiURL(model, "generateContent")
		body, err = json.Marshal(g)
		decode = func(raw []byte) (*wire.GeminiResponse, error) {
			var out wire.GeminiResponse
			if err := json.Unmarshal(raw, &out); err != nil {
				return nil, err
			}
			return &out, nil
		}
	default:
		req, buildErr := wire.ToOpenAI(g, model, false)
		if buildErr != nil {
			return buildErr
		}
		url = p.ChatURL()
		body, err = json.Marshal(req)
	}
	if err != nil {
		return err
	}

	fmt.Printf("  %s %s\n", dim("→"), dim(model+" at "+url))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	discover.Authorize(req, p, p.EnabledKeys()[0].Value)
	for _, h := range p.Headers {
		req.Header.Set(h.Name, h.Value)
	}

	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d — %s", resp.StatusCode, discover.Message(raw, resp.Status))
	}
	out, err := decode(raw)
	if err != nil {
		return err
	}
	said := ""
	if len(out.Candidates) > 0 {
		for _, part := range out.Candidates[0].Content.Parts {
			if part.Text != "" && !part.Thought {
				said += part.Text
			}
		}
	}
	if strings.TrimSpace(said) == "" {
		return fmt.Errorf("the host answered, but with no text in it")
	}
	fmt.Printf("  %s said %q %s\n", green("✓"), truncate(said, 60), dim(time.Since(started).Round(time.Millisecond).String()))
	return nil
}
