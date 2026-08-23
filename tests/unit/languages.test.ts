import { describe, expect, it } from 'vitest';
import type { FileInfo } from '../../src/lib/data/schema';
import { detectLanguage, isVendoredPath, languageStats } from '../../src/lib/ingest/languages';

function file(path: string, size: number, extra: Partial<FileInfo> = {}): FileInfo {
  return {
    path,
    sha: '0'.repeat(40),
    size,
    binary: false,
    tooLarge: false,
    stored: true,
    language: detectLanguage(path),
    ...extra,
  };
}

describe('languages', () => {
  it('detects languages by extension and filename', () => {
    expect(detectLanguage('src/index.ts')).toBe('TypeScript');
    expect(detectLanguage('a/b/c.tsx')).toBe('TypeScript');
    expect(detectLanguage('app.js')).toBe('JavaScript');
    expect(detectLanguage('main.py')).toBe('Python');
    expect(detectLanguage('main.go')).toBe('Go');
    expect(detectLanguage('lib.rs')).toBe('Rust');
    expect(detectLanguage('App.svelte')).toBe('Svelte');
    expect(detectLanguage('index.astro')).toBe('Astro');
    expect(detectLanguage('style.css')).toBe('CSS');
    expect(detectLanguage('style.scss')).toBe('SCSS');
    expect(detectLanguage('Dockerfile')).toBe('Dockerfile');
    expect(detectLanguage('docker/Dockerfile.dev')).toBe('Dockerfile');
    expect(detectLanguage('Makefile')).toBe('Makefile');
    expect(detectLanguage('CMakeLists.txt')).toBe('CMake');
    expect(detectLanguage('script.ps1')).toBe('PowerShell');
    expect(detectLanguage('run.bat')).toBe('Batchfile');
    expect(detectLanguage('README.md')).toBe('Markdown');
    expect(detectLanguage('package.json')).toBe('JSON');
    expect(detectLanguage('package-lock.json')).toBe('Lockfile');
    expect(detectLanguage('.gitignore')).toBe('Ignore List');
    expect(detectLanguage('notebook.ipynb')).toBe('Jupyter Notebook');
    expect(detectLanguage('LICENSE')).toBe('Text');
    expect(detectLanguage('flake.nix')).toBe('Nix');
    expect(detectLanguage('main.tf')).toBe('HCL');
    expect(detectLanguage('unknown.xyz')).toBeNull();
    expect(detectLanguage('noext')).toBeNull();
    expect(detectLanguage('.hidden')).toBeNull();
  });

  it('flags vendored paths', () => {
    expect(isVendoredPath('node_modules/foo/index.js')).toBe(true);
    expect(isVendoredPath('src/vendor/lib.js')).toBe(true);
    expect(isVendoredPath('dist/bundle.js')).toBe(true);
    expect(isVendoredPath('public/app.min.js')).toBe(true);
    expect(isVendoredPath('src/app.js')).toBe(false);
    expect(isVendoredPath('src/distribution.ts')).toBe(false);
  });

  it('computes stats excluding docs, config, vendored, binary and unknown files', () => {
    const stats = languageStats([
      file('src/a.ts', 600),
      file('src/b.ts', 150),
      file('src/c.js', 250),
      file('README.md', 5000),
      file('package.json', 5000),
      file('package-lock.json', 50000),
      file('config.yaml', 1000),
      file('node_modules/x/index.js', 9000),
      file('dist/bundle.js', 9000),
      file('app.min.js', 9000),
      file('img.png', 9000, { binary: true }),
      file('mystery.xyz', 9000),
      file('bin.ts', 9000, { binary: true }),
    ]);
    expect(stats).toEqual([
      { name: 'TypeScript', bytes: 750, percent: 75, color: '#3178c6' },
      { name: 'JavaScript', bytes: 250, percent: 25, color: '#f1e05a' },
    ]);
  });

  it('percents sum to 100 after rounding', () => {
    const stats = languageStats([file('a.ts', 1), file('b.js', 1), file('c.py', 1)]);
    expect(stats.map((s) => s.name)).toEqual(['JavaScript', 'Python', 'TypeScript']); // equal bytes → by name
    const sum = stats.reduce((a, s) => a + s.percent, 0);
    expect(Math.round(sum * 10) / 10).toBe(100);
    expect(stats[0]!.percent).toBeCloseTo(33.4, 5);
  });

  it('returns [] when nothing counts', () => {
    expect(languageStats([file('README.md', 10), file('x.png', 10, { binary: true })])).toEqual([]);
    expect(languageStats([])).toEqual([]);
  });
});
