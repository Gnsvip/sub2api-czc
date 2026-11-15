# AIFXO bundled guide

This is the approved fifteen-page AIFXO tutorial, served at `/guide-content/`
inside the public `/guide` route. `index.html`, `style.css`, `main.js`, and
`snippets.js` are maintained source files; Vite builds this as a separate HTML
entry. Do not edit generated files in `backend/internal/web/dist`.

- Keep the website at `https://www.aifxo.com/` and model API examples at
  `https://api.aifxo.com`. API credentials are placeholders only.
- Screenshots in `images/` are the approved cropped/redacted copies. Keep
  galleries collapsed and display one image at a time.
- Preserve the desktop Codex installation order and separate `config.toml` /
  `auth.json` instructions. The auth JSON example contains only `OPENAI_API_KEY`.
- The configuration-tool link uses an immutable GitHub release asset. Upload the
  existing 1.2.0 ZIP with that release; do not commit executable archives here.
- The outer guide chooses bundled/custom mode from the existing settings API.
  Stored Markdown is rendered by `CustomGuideView`, never executed here.
- Scripts remain external modules to work with production CSP. The backend
  permits same-origin framing only for this exact public tutorial entry.

GitHub Actions runs the existing frontend tests, the guide browser workflow
against built assets with production CSP, and the embedded Go route checks.
