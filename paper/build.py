#!/usr/bin/env python3
"""Build the manuscript, reproduce its checks, and prepare an arXiv upload."""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parent
BUILD = ROOT / 'build'
EVIDENCE = ('protocol_model.py', 'boundary_checks.py',
            'model-results.json', 'boundary-results.json', 'README.md')


def run(command, cwd=ROOT, log=None, env=None):
    result = subprocess.run(command, cwd=cwd, env=env, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if log:
        log.write_text(result.stdout)
    if result.returncode:
        raise RuntimeError(f"Command failed: {' '.join(map(str, command))}\n{result.stdout[-5000:]}")
    return result.stdout


def check_evidence():
    # Run in a temporary directory so a failed check cannot replace recorded results.
    with tempfile.TemporaryDirectory(prefix='talea-evidence-') as folder:
        scratch = Path(folder)
        for script, result in [('protocol_model.py', 'model-results.json'),
                               ('boundary_checks.py', 'boundary-results.json')]:
            shutil.copy2(ROOT / 'evidence' / script, scratch / script)
            run([sys.executable, script], cwd=scratch)
            actual = json.loads((scratch / result).read_text())
            expected = json.loads((ROOT / 'evidence' / result).read_text())
            if actual != expected:
                raise RuntimeError(f'{result} differs from the recorded evidence; review the manuscript before updating it.')
    print('Evidence checks reproduce both recorded result files.')


def build(engine):
    BUILD.mkdir(exist_ok=True)
    if engine == 'auto':
        engine = 'pdflatex' if shutil.which('pdflatex') and shutil.which('bibtex') else 'tectonic'
    if engine == 'pdflatex':
        if not shutil.which('pdflatex') or not shutil.which('bibtex'):
            raise RuntimeError('PDFLaTeX and BibTeX must be on PATH.')
        env = os.environ.copy()
        env['BIBINPUTS'] = str(ROOT) + os.pathsep + env.get('BIBINPUTS', '')
        latex = ['pdflatex', '-no-shell-escape', '-interaction=nonstopmode',
                 '-halt-on-error', '-file-line-error', '-recorder',
                 f'-output-directory={BUILD}', 'main.tex']
        run(latex, log=BUILD / 'pdflatex-1.txt', env=env)
        run(['bibtex', 'main'], cwd=BUILD, log=BUILD / 'bibtex.txt', env=env)
        for n in (2, 3):
            run(latex, log=BUILD / f'pdflatex-{n}.txt', env=env)
    else:
        if not shutil.which('tectonic'):
            raise RuntimeError('Install TeX Live (PDFLaTeX + BibTeX) or Tectonic, then rerun.')
        run(['tectonic', '--untrusted', '--keep-logs', '--keep-intermediates',
             '--outdir', str(BUILD), 'main.tex'], log=BUILD / 'tectonic.txt')
    log = (BUILD / 'main.log').read_text(errors='replace')
    if re.search(r'undefined (?:references|citations)|(?:Reference|Citation) .+ undefined|Overfull \\[hv]box|Missing character:|Label\(s\) may have changed|multiply defined', log, re.I):
        raise RuntimeError('Unresolved reference, missing glyph or layout warning; inspect build/main.log.')
    shutil.copy2(BUILD / 'main.pdf', ROOT / 'main.pdf')
    print(f'Built main.pdf using {engine}.')


def package():
    main = (ROOT / 'main.tex').read_text()
    abstract = re.search(r'\\begin\{abstract\}\s*(.*?)\s*\\end\{abstract\}', main, re.S).group(1)
    abstract = ' '.join(abstract.split())
    if len(abstract) > 1920 or not abstract.isascii():
        raise RuntimeError('The metadata abstract must be ASCII and no longer than 1920 characters.')
    # Traverse explicit inputs and figure references; include only actual dependencies.
    sources = set()
    figures = set()
    def collect(relative):
        if relative in sources:
            return
        sources.add(relative)
        text = (ROOT / relative).read_text()
        if re.search(r'TODO|FIXME|\[Affiliation\]|\[email\]', text):
            raise RuntimeError(f'Unresolved placeholder in {relative}.')
        for name in re.findall(r'\\input\{([^}]+)\}', text):
            collect(name if name.endswith('.tex') else name + '.tex')
        figures.update(re.findall(r'\\includegraphics(?:\[[^\]]*\])?\{([^}]+)\}', text))
    collect('main.tex')
    files = {name: ROOT / name for name in sources | figures | {'arxiv.sty', 'references.bib'}}
    files['main.bbl'] = BUILD / 'main.bbl'
    files.update({f'anc/{name}': ROOT / 'evidence' / name for name in EVIDENCE})
    archive = BUILD / 'arxiv-source.zip'
    with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as out:
        for name, path in sorted(files.items()):
            if not path.is_file():
                raise RuntimeError(f'Missing submission dependency: {path}')
            if any(part in {'..', ''} or not re.fullmatch(r'[a-zA-Z0-9_+.,=-]+', part)
                   for part in Path(name).parts):
                raise RuntimeError(f'Non-portable archive path: {name}')
            out.write(path, name)
    title = re.search(r'\\title\{([^}]+)\}', main).group(1).replace('\\\\', ' ')
    title = ' '.join(title.split())
    metadata = (f'Title: {title}\n\n'
                'Authors: Carlos Eberhardt (IBM Software, Rochester, United States of America), '
                'Mansura Habiba (IBM Software, Dublin, Ireland)\n\n'
                f'Abstract ({len(abstract)} characters):\n{abstract}\n\n'
                'Suggested primary category: cs.MA (Multiagent Systems)\n'
                'Compiler: pdflatex\nTop-level file: main.tex\n'
                'Comments: 7 figures, 4 tables; includes reproducible model and interface checks.\n'
                'License: select during submission; no license choice is assumed here.\n')
    (BUILD / 'arxiv-metadata.txt').write_text(metadata)
    print(f'Created {archive} ({len(files)} files); metadata abstract: {len(abstract)} characters.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', nargs='?', choices=['build', 'checks', 'figures', 'arxiv'], default='build')
    parser.add_argument('--engine', choices=['auto', 'pdflatex', 'tectonic'], default='auto')
    args = parser.parse_args()
    if args.action == 'checks':
        check_evidence()
    elif args.action == 'figures':
        for script in ('draw_a2b_gap.py', 'draw_a2b_figures.py'):
            run([sys.executable, str(ROOT / 'figures' / script)])
        print('Regenerated six PDF and SVG diagrams with embedded PDF fonts.')
    elif args.action == 'arxiv':
        check_evidence()
        build(args.engine)
        package()
    else:
        build(args.engine)


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, OSError) as error:
        sys.exit(str(error))
