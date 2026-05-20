package main

import (
	"encoding/json"
	"fmt"
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
		configPath string
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

			spec := buildSpec(modelDir, modelRef, name, engine, mode, group, host, port, def, draft, cfg)
			if err := validateNewBackend(spec, cfg); err != nil {
				return err
			}
			if err := appendBackend(configPath, spec); err != nil {
				return fmt.Errorf("append: %w", err)
			}
			fmt.Printf("added [[backend]] name=%q engine=%s mode=%s port=%d → %s\n",
				spec.Name, spec.Engine, spec.Mode, spec.Port, configPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "backend name (default: sanitized last path segment)")
	cmd.Flags().StringVar(&engine, "engine", "", "lm|vlm|embed|audio (default: auto-detect via config.json model_type)")
	cmd.Flags().StringVar(&mode, "mode", "", "swap|persistent (default: swap for lm/vlm, persistent for embed/audio)")
	cmd.Flags().StringVar(&group, "group", "", "swap group (default: chat for swap, name for persistent)")
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "host")
	cmd.Flags().IntVar(&port, "port", 0, "port (required for new persistent backends; new swap groups need --port too)")
	cmd.Flags().BoolVar(&def, "default", false, "mark as default member of its swap group")
	cmd.Flags().StringVar(&draft, "draft", "", "draft model path (engine=lm only)")
	cmd.Flags().BoolVar(&noDownload, "no-download", false, "for HF args: do not pre-download; let mlx_lm fetch lazily")
	cmd.Flags().StringVar(&configPath, "config", defaultConfigPathLocal(), "config.toml to modify")
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
					spec := buildSpec(c.path, c.path, c.name, c.engine, "", "", "127.0.0.1", 0, false, "", cfg)
					if err := validateNewBackend(spec, cfg); err != nil {
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
	cli := filepath.Join(filepath.Dir(pythonBin), "huggingface-cli")
	if _, err := os.Stat(cli); err != nil {
		return fmt.Errorf("huggingface-cli not found at %s. Install: %s -m pip install huggingface_hub", cli, pythonBin)
	}
	fmt.Printf("downloading %s → %s\n", repo, dest)
	cmd := exec.Command(cli, "download", repo, "--local-dir", dest)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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

func buildSpec(modelDir, modelRef, name, engine, mode, group, host string, port int, def bool, draft string, cfg *config.Config) config.BackendSpec {
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
	return config.BackendSpec{
		Name: name, Engine: engine, Mode: mode, Group: group,
		Host: host, Port: port, Model: modelRef, DraftModel: draft, Default: def,
	}
}

func validateNewBackend(spec config.BackendSpec, cfg *config.Config) error {
	for _, b := range cfg.Backends {
		if b.Name == spec.Name {
			return fmt.Errorf("backend name %q already exists", spec.Name)
		}
	}
	if spec.Mode == "swap" && spec.Port == 0 {
		return fmt.Errorf("swap mode requires --port (group %q has no existing port to inherit)", spec.Group)
	}
	if spec.Mode == "persistent" && spec.Port == 0 && spec.Engine != "audio" {
		return fmt.Errorf("persistent mode requires --port")
	}
	return nil
}

func appendBackend(path string, b config.BackendSpec) error {
	var sb strings.Builder
	sb.WriteString("\n[[backend]]\n")
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

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(sb.String())
	return err
}
