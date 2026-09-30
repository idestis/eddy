# Eddy landing page and docs

Plain static HTML, CSS and a little JS (theme toggle, copy buttons). There is no build step and no framework.

## Preview locally

```sh
python3 -m http.server -d landing 8000   # from the repo root
open http://localhost:8000/
```

## Deploy

GitHub Pages serves this folder as-is. The workflow uploads `landing/` as the Pages artifact, so the site is published at `https://idestis.github.io/eddy/`. All page and asset links are relative, so the site also works under any other path.

## Layout

- `index.html`: landing page. Hero UI is an HTML/CSS illustration, not a screenshot.
- `docs/`: six short pages with a shared layout (each page is a standalone file; keep the left nav in sync when adding one).
- `assets/site.css`, `assets/site.js`: styles (tokens from `docs/prototype/index.html`) and scripts.
- `assets/fonts/`: Geist and Geist Mono (latin, variable woff2) from `@fontsource-variable`, SIL OFL 1.1 (`OFL.txt`). No third-party requests.
- `assets/og.svg` and `assets/og.png`: social card. Most crawlers need the PNG, so regenerate it from the SVG if you change the copy.
- `404.html`: uses `<base href="/eddy/">` because GitHub Pages serves it at any depth. Change or remove that line if the site moves to a custom domain at the root, and it will not resolve correctly in a local preview.
- `robots.txt`, `sitemap.xml`: use the `idestis.github.io/eddy` base. Update them if the URL changes.
