# Design principles

1. Prefer `\input` over `\include` for multi-file papers built with Tectonic, so BibTeX runs only on the root `.aux`.
2. Avoid packages that still ship Latin-1 sources when targeting Tectonic/XeTeX, or wrap them with `\XeTeXdefaultencoding`.
3. Every bibliography entry used in citations must include a `year` field.
4. Keep governance notes (progress, wiki, capabilities) current when paper tooling or structure changes.
