package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/spf13/cobra"
)

// vlmModelTypes: substrings in model_type that signal a vision-language model.
var vlmModelTypes = []string{"_vl", "vision", "llava", "idefics", "paligemma", "internvl", "mllama"}

// vlmArchKeywords: substrings in architectures[] that signal vlm.
// ForConditionalGeneration is the giveaway for multi-modal generators.
var vlmArchKeywords = []string{"ForConditionalGeneration", "VisionLanguage", "MultiModal"}

// embedModelTypes: model_type values typical of embedding models.
// BERT and friends are also used as causal LMs occasionally — for those edge
// cases the user can override with --engine.
var embedModelTypes = []string{"bert", "roberta", "distilbert", "mpnet", "nomic_bert", "sentence_transformers"}

// audioModelTypes: known TTS/audio model_type strings.
var audioModelTypes = []string{"omnivoice", "kokoro"}

// newAddCmd registers a single backend in the config. The argument is either a
// local model directory or a Hugging Face repo id (org/repo).
func newAddCmd() *cobra.Command {
	var (
		name       string
		engine     string
		mode       string
		group      string
		host       string
		port       int
		def        bool
		draft      string
		noDownload bool
		noReload   bool
		overwrite  bool
		configPath string

		temperature float64
		topP        float64
		topK        int
		minP        float64
		repPenalty  float64
		maxTokens   int
	)
	cmd := &cobra.Command{
		Use:   "add <path-or-hf-repo>",
		Short: "Register a backend (downloads HF repos to models_root)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := args[0]

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			modelDir, modelRef, err := resolveModelArg(arg, cfg, noDownload)
			if err != nil {
				return err
			}

			spec, err := buildSpec(modelDir, modelRef, name, engine, mode, group, host, port, def, draft, cfg)
			if err != nil {
				return err
			}
			// autoPort is true only when buildSpec allocated a brand-new port.
			// An inherited swap-group port matches an existing backend, so it is
			// not "auto" — the group chose it.
			autoPort := port == 0 && spec.Port != 0
			for _, b := range cfg.Backends {
				if b.Port == spec.Port {
					autoPort = false
					break
				}
			}
			spec.Sampler = samplerFromFlags(temperature, topP, topK, minP, repPenalty, maxTokens)

			exists := false
			for _, b := range cfg.Backends {
				if b.Name == spec.Name {
					exists = true
					break
				}
			}
			if exists && !overwrite {
				return fmt.Errorf("backend name %q already exists (pass --overwrite to replace)", spec.Name)
			}
			if err := validateNewBackend(spec); err != nil {
				return err
			}

			verb := "added"
			if exists {
				if err := replaceBackend(configPath, spec.Name, spec); err != nil {
					return fmt.Errorf("replace: %w", err)
				}
				verb = "updated"
			} else if err := appendBackend(configPath, spec); err != nil {
				return fmt.Errorf("append: %w", err)
			}
			portNote := ""
			if autoPort {
				portNote = " (auto)"
			}
			fmt.Printf("%s [[backend]] name=%q engine=%s mode=%s port=%d%s → %s\n",
				verb, spec.Name, spec.Engine, spec.Mode, spec.Port, portNote, configPath)

			// Hot-reload the running daemon so the backend is usable now. Best
			// effort: a down daemon must never fail the config write. Reload is
			// additive, so an --overwrite edit needs a restart to take effect.
			if exists {
				fmt.Println("updated an existing backend; restart mlxd to apply (hot reload is additive only)")
			} else if !noReload {
				cx, cancel := ctx()
				defer cancel()
				res, down, rerr := callReload(cx)
				switch {
				case rerr != nil:
					fmt.Fprintf(os.Stderr, "warning: %v\n", rerr)
				case down:
					fmt.Println("mlxd not running; takes effect on next start")
				default:
					printReload(res)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "backend name (default: sanitized last path segment)")
	cmd.Flags().StringVar(&engine, "engine", "", "lm|vlm|embed|audio (default: auto-detect via config.json model_type)")
	cmd.Flags().StringVar(&mode, "mode", "", "swap|persistent (default: swap for lm/vlm, persistent for embed/audio)")
	cmd.Flags().StringVar(&group, "group", "", "swap group (default: chat for swap, name for persistent)")
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "host")
	cmd.Flags().IntVar(&port, "port", 0, "upstream port (default: auto-allocated free high port; swap members inherit their group's)")
	cmd.Flags().BoolVar(&def, "default", false, "mark as default member of its swap group")
	cmd.Flags().StringVar(&draft, "draft", "", "draft model path (engine=lm only)")
	cmd.Flags().BoolVar(&noDownload, "no-download", false, "for HF args: do not pre-download; let mlx_lm fetch lazily")
	cmd.Flags().BoolVar(&noReload, "no-reload", false, "don't hot-reload the running mlxd after writing config")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "replace an existing backend of the same name in place")
	cmd.Flags().StringVar(&configPath, "config", defaultConfigPathLocal(), "config.toml to modify")

	// Sampler defaults written to [backend.sampler]. Omitted fields (left at 0)
	// fall through to mlx_lm's own CLI defaults.
	cmd.Flags().Float64Var(&temperature, "temperature", 0, "sampler: temperature")
	cmd.Flags().Float64Var(&topP, "top-p", 0, "sampler: top-p (nucleus)")
	cmd.Flags().IntVar(&topK, "top-k", 0, "sampler: top-k")
	cmd.Flags().Float64Var(&minP, "min-p", 0, "sampler: min-p")
	cmd.Flags().Float64Var(&repPenalty, "repetition-penalty", 0, "sampler: repetition penalty")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 0, "sampler: max tokens per response")
	return cmd
}

// newScanCmd walks a directory of models. With --add, appends missing ones to the config.
func newScanCmd() *cobra.Command {
	var (
		add        bool
		configPath string
	)
	cmd := &cobra.Command{
		Use:   "scan [<dir>]",
		Short: "List models under a dir; --add appends missing ones",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			dir := cfg.ModelsRoot
			if len(args) >= 1 {
				dir = args[0]
			}
			if dir == "" {
				return fmt.Errorf("no scan dir (set models_root in config or pass <dir>)")
			}

			entries, err := os.ReadDir(dir)
			if err != nil {
				return fmt.Errorf("read dir: %w", err)
			}

			// Build a set of model paths already in config for quick membership check.
			registered := map[string]bool{}
			for _, b := range cfg.Backends {
				if b.Model != "" {
					registered[filepath.Clean(b.Model)] = true
				}
			}

			type candidate struct {
				path, name, engine string
				inConfig           bool
			}
			var cands []candidate
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				p := filepath.Join(dir, e.Name())
				if _, err := os.Stat(filepath.Join(p, "config.json")); err != nil {
					continue
				}
				eng := detectEngine(p)
				cands = append(cands, candidate{
					path:     p,
					name:     sanitizeName(e.Name()),
					engine:   eng,
					inConfig: registered[filepath.Clean(p)],
				})
			}
			sort.Slice(cands, func(i, j int) bool { return cands[i].name < cands[j].name })

			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tENGINE\tIN CONFIG\tPATH")
			added := 0
			for _, c := range cands {
				inCfg := "yes"
				if !c.inConfig {
					inCfg = "no"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.name, defaultStr(c.engine, "?"), inCfg, c.path)
				if add && !c.inConfig {
					if c.engine == "" {
						fmt.Fprintf(os.Stderr, "skip %s: could not detect engine (config.json missing model_type?); add manually with --engine\n", c.name)
						continue
					}
					spec, err := buildSpec(c.path, c.path, c.name, c.engine, "", "", "127.0.0.1", 0, false, "", cfg)
					if err != nil {
						fmt.Fprintf(os.Stderr, "skip %s: %v\n", c.name, err)
						continue
					}
					if err := validateNewBackend(spec); err != nil {
						fmt.Fprintf(os.Stderr, "skip %s: %v\n", c.name, err)
						continue
					}
					if err := appendBackend(configPath, spec); err != nil {
						fmt.Fprintf(os.Stderr, "skip %s: append: %v\n", c.name, err)
						continue
					}
					added++
					// Refresh local view so the next iteration sees this group's port for shared swap members.
					cfg.Backends = append(cfg.Backends, spec)
				}
			}
			tw.Flush()
			if add {
				fmt.Printf("\nadded %d backend(s) to %s\n", added, configPath)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&add, "add", false, "append missing entries to the config")
	cmd.Flags().StringVar(&configPath, "config", defaultConfigPathLocal(), "config.toml to read/modify")
	return cmd
}

func defaultConfigPathLocal() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "mlx", "config.toml")
}

// resolveModelArg figures out whether arg is a local path or an HF repo id.
// For HF: downloads to models_root/<repo-tail> unless noDownload is set.
// Returns (localDir, modelRefForConfig, error). When noDownload is set for an
// HF repo, localDir is "" and modelRef is the repo id (mlx_lm can fetch lazily).
func resolveModelArg(arg string, cfg *config.Config, noDownload bool) (string, string, error) {
	// Local path heuristic
	if strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "~") || strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") {
		p := expandHomeLocal(arg)
		if _, err := os.Stat(p); err != nil {
			return "", "", fmt.Errorf("local path %s: %w", p, err)
		}
		return p, p, nil
	}
	// Looks like HF repo (contains slash and no path-like leading)?
	if strings.Contains(arg, "/") {
		if noDownload {
			return "", arg, nil
		}
		if cfg.ModelsRoot == "" {
			return "", "", fmt.Errorf("HF download needs models_root set in config")
		}
		tail := arg[strings.LastIndex(arg, "/")+1:]
		dest := filepath.Join(cfg.ModelsRoot, tail)
		if _, err := os.Stat(filepath.Join(dest, "config.json")); err == nil {
			fmt.Printf("%s already present at %s; skipping download\n", arg, dest)
			return dest, dest, nil
		}
		if err := downloadHF(arg, dest, cfg.PythonBin); err != nil {
			return "", "", fmt.Errorf("download %s: %w", arg, err)
		}
		return dest, dest, nil
	}
	// Bare name: try cfg.ModelsRoot/<name>
	if cfg.ModelsRoot != "" {
		p := filepath.Join(cfg.ModelsRoot, arg)
		if _, err := os.Stat(p); err == nil {
			return p, p, nil
		}
	}
	return "", "", fmt.Errorf("could not resolve %q: not an absolute path, not an HF repo (no /), and not present in models_root", arg)
}

func expandHomeLocal(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func downloadHF(repo, dest, pythonBin string) error {
	cli, err := hfCLI(pythonBin)
	if err != nil {
		return err
	}
	fmt.Printf("downloading %s → %s\n", repo, dest)
	cmd := exec.Command(cli, "download", repo, "--local-dir", dest)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// hfCLI locates the Hugging Face CLI (`hf`, which replaced the deprecated
// `huggingface-cli`). Prefers the binary installed alongside pythonBin (the
// console script pip drops into the same venv bin/), then falls back to `hf`
// on PATH for standalone installs.
func hfCLI(pythonBin string) (string, error) {
	local := filepath.Join(filepath.Dir(pythonBin), "hf")
	if _, err := os.Stat(local); err == nil {
		return local, nil
	}
	if p, err := exec.LookPath("hf"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("hf CLI not found next to %s or on PATH (the `hf` command replaced the deprecated `huggingface-cli`). Upgrade: %s -m pip install --upgrade 'huggingface_hub>=0.34'", pythonBin, pythonBin)
}

func detectEngine(modelDir string) string {
	cfgPath := filepath.Join(modelDir, "config.json")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return ""
	}
	var c struct {
		ModelType     string   `json:"model_type"`
		Architectures []string `json:"architectures"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return ""
	}
	mt := strings.ToLower(c.ModelType)

	for _, a := range audioModelTypes {
		if mt == a {
			return "audio"
		}
	}
	// Architecture wins for vlm: ForConditionalGeneration etc. is decisive.
	for _, arch := range c.Architectures {
		for _, kw := range vlmArchKeywords {
			if strings.Contains(arch, kw) {
				return "vlm"
			}
		}
	}
	for _, v := range vlmModelTypes {
		if strings.Contains(mt, v) {
			return "vlm"
		}
	}
	for _, e := range embedModelTypes {
		if mt == e {
			return "embed"
		}
	}
	if mt != "" {
		return "lm"
	}
	return ""
}

func sanitizeName(s string) string {
	s = strings.ToLower(s)
	// Replace anything that's not letter/digit/dash/underscore with dash.
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
	}
	out := sb.String()
	out = strings.Trim(out, "-")
	return out
}

func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func buildSpec(modelDir, modelRef, name, engine, mode, group, host string, port int, def bool, draft string, cfg *config.Config) (config.BackendSpec, error) {
	if name == "" {
		if modelDir != "" {
			name = sanitizeName(filepath.Base(modelDir))
		} else {
			// HF no-download case: derive from modelRef.
			tail := modelRef[strings.LastIndex(modelRef, "/")+1:]
			name = sanitizeName(tail)
		}
	}
	if engine == "" && modelDir != "" {
		engine = detectEngine(modelDir)
	}
	if engine == "" {
		engine = "lm" // best-effort default
	}
	if mode == "" {
		switch engine {
		case "lm", "vlm":
			mode = "swap"
		default:
			mode = "persistent"
		}
	}
	if group == "" {
		if mode == "swap" {
			group = "chat"
		} else {
			group = name
		}
	}
	if port == 0 {
		// For swap: try to inherit the group's port from existing config.
		if mode == "swap" {
			for _, b := range cfg.Backends {
				if b.Mode == "swap" && b.Group == group {
					port = b.Port
					break
				}
			}
		}
	}
	if port == 0 {
		// Internal upstream port — the router fronts every backend, so the
		// number is plumbing the user shouldn't have to pick. Grab a free high
		// one that doesn't collide with anything already in the config.
		p, err := allocatePort(host, usedPorts(cfg))
		if err != nil {
			return config.BackendSpec{}, err
		}
		port = p
	}
	return config.BackendSpec{
		Name: name, Engine: engine, Mode: mode, Group: group,
		Host: host, Port: port, Model: modelRef, DraftModel: draft, Default: def,
	}, nil
}

// usedPorts collects every port the config already commits to: the router's
// own port, its extra ports, and each backend's upstream port. allocatePort
// avoids these so a fresh backend never shadows an existing listener.
func usedPorts(cfg *config.Config) map[int]bool {
	used := map[int]bool{}
	if cfg.Router.Port > 0 {
		used[cfg.Router.Port] = true
	}
	for _, p := range cfg.Router.ExtraPorts {
		used[p] = true
	}
	for _, b := range cfg.Backends {
		if b.Port > 0 {
			used[b.Port] = true
		}
	}
	return used
}

// allocatePort returns a free high port on host. The kernel hands out an
// ephemeral port via :0 (guaranteed bindable right now); we re-roll if it
// happens to match a port the config already claims but isn't listening on.
func allocatePort(host string, used map[int]bool) (int, error) {
	for range 50 {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			return 0, fmt.Errorf("auto-allocate port: %w", err)
		}
		p := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		if !used[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("auto-allocate port: no free port outside the %d configured ports after 50 tries", len(used))
}

// validateNewBackend checks mode/port constraints. The caller handles
// duplicate-name policy (reject vs --overwrite) before this runs. buildSpec
// always resolves a port (explicit, inherited, or auto-allocated), so a zero
// here means an internal bug rather than missing user input.
func validateNewBackend(spec config.BackendSpec) error {
	if spec.Mode != "external" && spec.Port == 0 {
		return fmt.Errorf("internal: backend %q has no port after spec build", spec.Name)
	}
	return nil
}

// samplerFromFlags returns a *Sampler if any field is set, else nil. Zero is
// the "unset" sentinel here, matching the rest of the system where zero-valued
// sampler params are omitted and mlx_lm's CLI defaults apply.
func samplerFromFlags(temperature, topP float64, topK int, minP, repPenalty float64, maxTokens int) *config.Sampler {
	if temperature == 0 && topP == 0 && topK == 0 && minP == 0 && repPenalty == 0 && maxTokens == 0 {
		return nil
	}
	return &config.Sampler{
		Temperature:       temperature,
		TopP:              topP,
		TopK:              topK,
		MinP:              minP,
		RepetitionPenalty: repPenalty,
		MaxTokens:         maxTokens,
	}
}

// renderBackend serializes one backend as a [[backend]] block. The returned
// text starts with the "[[backend]]" header and ends with a trailing newline;
// callers add any leading separator they need.
func renderBackend(b config.BackendSpec) string {
	var sb strings.Builder
	sb.WriteString("[[backend]]\n")
	sb.WriteString(fmt.Sprintf("name   = %q\n", b.Name))
	sb.WriteString(fmt.Sprintf("engine = %q\n", b.Engine))
	sb.WriteString(fmt.Sprintf("mode   = %q\n", b.Mode))
	if b.Group != "" && b.Group != b.Name {
		sb.WriteString(fmt.Sprintf("group  = %q\n", b.Group))
	}
	if b.Default {
		sb.WriteString("default = true\n")
	}
	sb.WriteString(fmt.Sprintf("host   = %q\n", b.Host))
	if b.Port > 0 {
		sb.WriteString(fmt.Sprintf("port   = %d\n", b.Port))
	}
	if b.Model != "" {
		sb.WriteString(fmt.Sprintf("model  = %q\n", b.Model))
	}
	if b.DraftModel != "" {
		sb.WriteString(fmt.Sprintf("draft_model = %q\n", b.DraftModel))
	}
	if s := b.Sampler; s != nil {
		sb.WriteString("  [backend.sampler]\n")
		if s.Temperature != 0 {
			sb.WriteString(fmt.Sprintf("  temperature        = %g\n", s.Temperature))
		}
		if s.TopP != 0 {
			sb.WriteString(fmt.Sprintf("  top_p              = %g\n", s.TopP))
		}
		if s.TopK != 0 {
			sb.WriteString(fmt.Sprintf("  top_k              = %d\n", s.TopK))
		}
		if s.MinP != 0 {
			sb.WriteString(fmt.Sprintf("  min_p              = %g\n", s.MinP))
		}
		if s.RepetitionPenalty != 0 {
			sb.WriteString(fmt.Sprintf("  repetition_penalty = %g\n", s.RepetitionPenalty))
		}
		if s.MaxTokens != 0 {
			sb.WriteString(fmt.Sprintf("  max_tokens         = %d\n", s.MaxTokens))
		}
	}
	return sb.String()
}

func appendBackend(path string, b config.BackendSpec) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\n" + renderBackend(b))
	return err
}

// replaceBackend rewrites the existing [[backend]] block named name in place,
// preserving every other line (comments, other backends, spacing) untouched.
func replaceBackend(path, name string, b config.BackendSpec) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(data)
	trailingNL := strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")

	start, end, ok := backendBlockSpan(lines, name)
	if !ok {
		return fmt.Errorf("backend %q not found in %s", name, path)
	}

	block := strings.Split(strings.TrimRight(renderBackend(b), "\n"), "\n")
	out := make([]string, 0, len(lines)+len(block))
	out = append(out, lines[:start]...)
	out = append(out, block...)
	out = append(out, lines[end:]...)

	res := strings.Join(out, "\n")
	if trailingNL {
		res += "\n"
	}
	return os.WriteFile(path, []byte(res), 0o644)
}

// backendBlockSpan locates the [[backend]] block declaring name and returns its
// half-open line range [start, end), with trailing blank lines excluded so the
// separator before the following section survives a splice. ok is false when no
// block declares that name.
func backendBlockSpan(lines []string, name string) (start, end int, ok bool) {
	// endsBlock reports whether a line opens a new TOML section, closing the
	// current backend. "[backend.sampler]" and friends are subtables, so they
	// stay inside the block.
	endsBlock := func(s string) bool {
		t := strings.TrimSpace(s)
		if strings.HasPrefix(t, "[[") {
			return true
		}
		return strings.HasPrefix(t, "[") && !strings.HasPrefix(t, "[backend.")
	}
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "[[backend]]" {
			continue
		}
		j := i + 1
		for j < len(lines) && !endsBlock(lines[j]) {
			j++
		}
		if blockDeclaresName(lines[i+1:j], name) {
			// Back up over trailing blank and comment lines: a comment just
			// before the next section is conventionally that section's header,
			// not this block's content, so it must survive the splice.
			k := j
			for k > i+1 {
				t := strings.TrimSpace(lines[k-1])
				if t == "" || strings.HasPrefix(t, "#") {
					k--
					continue
				}
				break
			}
			return i, k, true
		}
		i = j - 1
	}
	return 0, 0, false
}

func blockDeclaresName(blockLines []string, name string) bool {
	for _, l := range blockLines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "[") {
			continue
		}
		eq := strings.Index(t, "=")
		if eq < 0 {
			continue
		}
		if strings.TrimSpace(t[:eq]) != "name" {
			continue
		}
		val := strings.TrimSpace(t[eq+1:])
		// Drop any inline comment, then surrounding quotes.
		if h := strings.Index(val, "#"); h >= 0 {
			val = strings.TrimSpace(val[:h])
		}
		if strings.Trim(val, `"`) == name {
			return true
		}
	}
	return false
}
