# V4 LaTeX build notes

## Toolchain

- Editor recipe: LaTeX Workshop → Tectonic (`/opt/homebrew/bin/tectonic`).
- Root file: `paper/main.tex`.
- Bibliography: single `\bibliography{references}` at end of `main.tex`.

## Why `\input` instead of `\include`

`\include` writes a separate `.aux` per chapter. Tectonic discovers every `.aux` and runs BibTeX on each. Chapter aux files have no `\bibdata`/`\bibstyle`, which produced the “errors were issued by BibTeX” noise and empty `.bbl` skip messages. `\input` keeps one aux chain rooted at `main.aux`.

## algorithm2e UTF-8

`algorithm2e.sty` still contains Latin-1 bytes. Tectonic surfaces XeTeX’s “Invalid UTF-8 byte” replacements as warnings. Temporary encoding switch around `\usepackage{algorithm2e}` clears them without vendoring the sty file.
