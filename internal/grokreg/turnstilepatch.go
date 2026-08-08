package grokreg

import (
	"embed"
	"os"
	"path/filepath"
)

//go:embed turnstilepatch/manifest.json turnstilepatch/script.js
var turnstilePatchFS embed.FS

// extractTurnstilePatch writes the embedded Turnstile-Patch extension to a fresh
// temp directory and returns its path. The extension rewrites
// MouseEvent.screenX/screenY at document_start in every frame, so Cloudflare's
// invisible managed Turnstile issues a token to a genuine checkbox click without
// any third-party solver — mirroring the reference project's turnstilePatch.
func extractTurnstilePatch() (string, error) {
	dir, err := os.MkdirTemp("", "turnstilepatch-*")
	if err != nil {
		return "", err
	}
	for _, name := range []string{"manifest.json", "script.js"} {
		data, rerr := turnstilePatchFS.ReadFile("turnstilepatch/" + name)
		if rerr != nil {
			os.RemoveAll(dir)
			return "", rerr
		}
		if werr := os.WriteFile(filepath.Join(dir, name), data, 0o644); werr != nil {
			os.RemoveAll(dir)
			return "", werr
		}
	}
	return dir, nil
}
