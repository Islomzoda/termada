# Reproducible terminal demo for the README GIF.
# Render it with charmbracelet/vhs:  vhs docs/demo.tape  ->  docs/demo.gif
# (vhs needs ffmpeg + ttyd; see https://github.com/charmbracelet/vhs)

Output docs/demo.gif
Set FontSize 16
Set Width 1180
Set Height 680
Set Padding 18
Set Theme "Dracula"

Type "# Install termada — one line, no Go needed"
Enter
Sleep 800ms
Type "curl -fsSL https://raw.githubusercontent.com/Islomzoda/termada/main/install.sh | sh"
Enter
Sleep 4s

Type "# Start the daemon + dashboard"
Enter
Type "termada serve >/tmp/termada.log 2>&1 &"
Enter
Sleep 2s

Type "# One-command health check"
Enter
Type "termada doctor"
Enter
Sleep 3s

Type "# Get the dashboard link (no token on your own machine)"
Enter
Type "termada dashboard"
Enter
Sleep 3s
