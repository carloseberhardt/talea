# Progress

## 2026-10-17 — Promote paper V4 to paper/

- Status: done (commit `e2bb265` on `main`, not pushed)
- Ignored `paper/V2/`, `paper/v3/`, `paper/V3/` and LaTeX intermediates.
- Moved former `paper/V4/*` to `paper/` (sources, figures, `main_v4.pdf`).
- Updated governance paths to `paper/`.

## 2026-10-17 — Table 1 / Figure 1 gap

- Status: done
- Cause: both were floats dumped onto a sparse float page with stretch between them.
- Fix: place Table 1 and Figure 1 inline via `\captionof` + `minipage`, with body text between them.

## 2026-10-17 — V4 float placement and lean captions

- Status: done
- Root cause: `booking-sequence` / reservation figures were too tall with long captions, so LaTeX deferred them to pages after the bibliography.
- Lean captions on all V4 figures and tables; `[htbp]` placement; height-capped `\includegraphics` so floats stay near callouts.

## 2026-10-17 — V4 LaTeX compiler cleanup

- Status: done
- Changed `\include` → `\input` in `paper/main.tex` so Tectonic no longer BibTeX-runs chapter aux files.
- Wrapped `algorithm2e` with `\XeTeXdefaultencoding` latin1/utf8 to clear Invalid UTF-8 warnings.
- Removed redundant `inputenc` (ignored under Tectonic/XeTeX).
- Added missing `year` fields for `mcp2026spec`, `ucpspec`, `acpspec`, `robustdecl`, `substrateoverview`, `substratearchitecture`.
- Verified clean `tectonic` build with no BibTeX/UTF-8 warnings.
- GitHub project item: blocked — `gh` token for github.ibm.com is invalid (`gh auth refresh -h github.ibm.com`).
