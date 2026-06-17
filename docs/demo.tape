# Reproducible terminal demo for the README GIF.
# Render with charmbracelet/vhs:  vhs docs/demo.tape  ->  docs/demo.gif
#   (needs vhs + ffmpeg + ttyd; assumes `termada` is on PATH and a daemon can start)
# Or headless via the official image:
#   docker run --rm -v "$PWD/docs":/vhs ghcr.io/charmbracelet/vhs demo.tape

Output demo.gif
Set Shell "bash"
Set FontSize 17
Set Width 1200
Set Height 720
Set Padding 22
Set Theme "Dracula"

Hide
Type "termada serve >/tmp/t.log 2>&1 & sleep 1; clear" Enter
Sleep 1.5s
Show

Type "termada version" Enter
Sleep 800ms
Type "# one-command health check:" Enter
Type "termada doctor" Enter
Sleep 2.5s
Type "# open the live dashboard (no token on your own machine):" Enter
Type "termada dashboard" Enter
Sleep 2.5s
