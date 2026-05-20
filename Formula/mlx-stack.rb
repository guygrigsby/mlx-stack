class MlxStack < Formula
  desc "Single-daemon supervisor + CLI for local MLX inference"
  homepage "https://github.com/guygrigsby/mlx-stack"
  license "MIT"
  head "https://github.com/guygrigsby/mlx-stack.git", branch: "main"

  depends_on "go" => :build

  def install
    ENV["CGO_ENABLED"] = "0"
    system "go", "build", *std_go_args(output: bin/"mlxd",   ldflags: "-s -w"), "./cmd/mlxd"
    system "go", "build", *std_go_args(output: bin/"mlxctl", ldflags: "-s -w"), "./cmd/mlxctl"

    (share/"mlx-stack/python").install Dir["python/*"]
    (share/"mlx-stack/deploy").install Dir["deploy/*"]
  end

  def caveats
    <<~EOS
      mlxd auto-detects the bundled Python shim at #{share}/mlx-stack/python —
      no pip install needed.

      It still needs a Python with mlx_lm / mlx_vlm / mlx_embeddings / mlx_audio
      installed. If you don't already have one:

          mlxctl bootstrap --path ~/venvs/mlx

      Then point python_bin at it in ~/.config/mlx/config.toml. To convert a
      legacy mlx.conf:

          mlxctl config migrate ~/.config/mlx.conf > ~/.config/mlx/config.toml

      Autostart on login (optional):

          mkdir -p ~/Library/LaunchAgents ~/.logs/mlx
          sed -e "s|{{INSTALL_DIR}}|#{bin}|g" -e "s|{{HOME}}|$HOME|g" \\
              "#{share}/mlx-stack/deploy/dev.grigsby.mlxd.plist.template" \\
              > ~/Library/LaunchAgents/dev.grigsby.mlxd.plist
          launchctl load ~/Library/LaunchAgents/dev.grigsby.mlxd.plist
    EOS
  end

  test do
    # mlxctl prints usage and exits 2 with no subcommand.
    assert_match "usage: mlxctl", shell_output("#{bin}/mlxctl 2>&1", 2)
    # mlxd prints usage and exits 2 with no subcommand.
    assert_match "usage: mlxd run", shell_output("#{bin}/mlxd 2>&1", 2)
  end
end
