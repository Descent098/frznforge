import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { detectSpdx, findLicenseEntry, isLicenseFilename, resolveLicense } from '../../src/lib/ingest/license';
import { findReadmeEntry } from '../../src/lib/ingest/readme';
import { scanRepo } from '../../src/lib/ingest/scan';
import { listRootTree } from '../../src/lib/ingest/tree';
import { FixtureRepo } from './helpers/fixture-repo';

const MIT = `MIT License

Copyright (c) 2024 Someone

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction...
`;

const APACHE = `                                 Apache License
                           Version 2.0, January 2004
                        http://www.apache.org/licenses/

   TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION
`;

const GPL3 = `                    GNU GENERAL PUBLIC LICENSE
                       Version 3, 29 June 2007
`;

const BSD3 = `Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:
3. Neither the name of the copyright holder nor the names of its
   contributors may be used to endorse or promote products derived from
   this software without specific prior written permission.
`;

const opts = { maxBlobBytes: 1024 * 1024, maxCommits: null };

describe('license detection', () => {
  it('recognises common license texts', () => {
    expect(detectSpdx(MIT)).toBe('MIT');
    expect(detectSpdx(APACHE)).toBe('Apache-2.0');
    expect(detectSpdx(GPL3)).toBe('GPL-3.0-only');
    expect(detectSpdx('GNU LESSER GENERAL PUBLIC LICENSE\nVersion 3, 29 June 2007')).toBe('LGPL-3.0-only');
    expect(detectSpdx('GNU AFFERO GENERAL PUBLIC LICENSE\nVersion 3, 19 November 2007')).toBe('AGPL-3.0-only');
    expect(detectSpdx('GNU GENERAL PUBLIC LICENSE\nVersion 2, June 1991')).toBe('GPL-2.0-only');
    expect(detectSpdx(BSD3)).toBe('BSD-3-Clause');
    expect(detectSpdx('Redistribution and use in source and binary forms, with or without modification, are permitted')).toBe('BSD-2-Clause');
    expect(detectSpdx('Mozilla Public License Version 2.0\n==================================')).toBe('MPL-2.0');
    expect(detectSpdx('ISC License\n\nCopyright (c) 2024')).toBe('ISC');
    expect(detectSpdx('This is free and unencumbered software released into the public domain.')).toBe('Unlicense');
    expect(detectSpdx('CC0 1.0 Universal')).toBe('CC0-1.0');
    expect(detectSpdx('Zero-Clause BSD\n=============')).toBe('0BSD');
    expect(detectSpdx('All rights reserved. Do not copy.')).toBeNull();
  });

  it('matches license filenames case-insensitively', () => {
    for (const n of ['LICENSE', 'license', 'LICENSE.md', 'LICENSE.txt', 'LICENCE', 'COPYING', 'COPYING.LESSER', 'UNLICENSE', 'LICENSE-MIT']) {
      expect(isLicenseFilename(n), n).toBe(true);
    }
    expect(isLicenseFilename('LICENSES.md')).toBe(false);
    expect(isLicenseFilename('README.md')).toBe(false);
  });

  it('resolveLicense honours config override', () => {
    expect(resolveLicense({ file: 'LICENSE', spdx: 'MIT' }, null)).toEqual({ spdx: 'MIT', file: 'LICENSE', source: 'file' });
    expect(resolveLicense({ file: 'LICENSE', spdx: null }, 'Apache-2.0')).toEqual({ spdx: 'Apache-2.0', file: 'LICENSE', source: 'config' });
    expect(resolveLicense(null, 'MIT')).toEqual({ spdx: 'MIT', file: null, source: 'config' });
    expect(resolveLicense(null, null)).toBeNull();
  });
});

describe('license + readme in a repo', () => {
  let mit: FixtureRepo;
  let apache: FixtureRepo;
  let none: FixtureRepo;

  beforeAll(() => {
    mit = FixtureRepo.create('mit');
    mit.writeAndCommit({ LICENSE: MIT, 'README.md': '# MIT repo\n', README: 'plain readme\n', 'readme.txt': 'txt\n' }, 'init');
    apache = FixtureRepo.create('apache');
    apache.writeAndCommit({ 'LICENSE.txt': APACHE, 'readme.md': '# lower\n' }, 'init');
    none = FixtureRepo.create('none');
    none.writeAndCommit({ 'src/LICENSE': MIT, 'docs/README.md': '# nested\n' }, 'init');
  });
  afterAll(() => {
    mit.cleanup();
    apache.cleanup();
    none.cleanup();
  });

  it('finds root license + readme entries, preferring README.md', async () => {
    const root = await listRootTree(mit.dir, 'main');
    expect(findLicenseEntry(root)!.path).toBe('LICENSE');
    expect(findReadmeEntry(root)!.path).toBe('README.md');
    const apRoot = await listRootTree(apache.dir, 'main');
    expect(findLicenseEntry(apRoot)!.path).toBe('LICENSE.txt');
    expect(findReadmeEntry(apRoot)!.path).toBe('readme.md');
    const noneRoot = await listRootTree(none.dir, 'main');
    expect(findLicenseEntry(noneRoot)).toBeNull();
    expect(findReadmeEntry(noneRoot)).toBeNull();
  });

  it('scanRepo reports detected licenses and readme content', async () => {
    const a = await scanRepo({ absPath: mit.dir }, opts);
    if ('skipped' in a) throw new Error('skipped');
    expect(a.repo.license).toEqual({ spdx: 'MIT', file: 'LICENSE', source: 'file' });
    expect(a.repo.readme).toMatchObject({ path: 'README.md', content: '# MIT repo\n' });
    expect(a.repo.readme!.sha).toBe(a.repo.files['README.md']!.sha);

    const b = await scanRepo({ absPath: apache.dir }, opts);
    if ('skipped' in b) throw new Error('skipped');
    expect(b.repo.license).toEqual({ spdx: 'Apache-2.0', file: 'LICENSE.txt', source: 'file' });
    expect(b.repo.readme!.path).toBe('readme.md');

    const c = await scanRepo({ absPath: none.dir }, opts);
    if ('skipped' in c) throw new Error('skipped');
    expect(c.repo.license).toBeNull();
    expect(c.repo.readme).toBeNull();
  });

  it('config override wins but keeps the detected file', async () => {
    const a = await scanRepo({ absPath: mit.dir, overrides: { license: 'BSD-3-Clause' } }, opts);
    if ('skipped' in a) throw new Error('skipped');
    expect(a.repo.license).toEqual({ spdx: 'BSD-3-Clause', file: 'LICENSE', source: 'config' });
    const c = await scanRepo({ absPath: none.dir, overrides: { license: 'MIT' } }, opts);
    if ('skipped' in c) throw new Error('skipped');
    expect(c.repo.license).toEqual({ spdx: 'MIT', file: null, source: 'config' });
  });

  it('readme over the blob size cap is null', async () => {
    const a = await scanRepo({ absPath: mit.dir }, { maxBlobBytes: 4, maxCommits: null });
    if ('skipped' in a) throw new Error('skipped');
    expect(a.repo.readme).toBeNull();
    expect(a.repo.license).toEqual({ spdx: 'MIT', file: 'LICENSE', source: 'file' });
  });
});
