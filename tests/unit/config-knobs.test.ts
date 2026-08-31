/**
 * The 0.2.0 config knobs: `theme.heat` (recency-accent day boundaries) and
 * `ingest.maxCommitAgeDays` (ingest timeframe limit). Site-config only — neither touches
 * the artifact schema, so there is no SCHEMA_VERSION interplay to test here.
 */
import { describe, expect, it } from 'vitest';
import { FrznforgeConfigSchema } from '../../src/lib/config/schema';

const base = { owner: { name: 'Owner', handle: 'owner' } };

describe('theme.heat', () => {
  it('defaults to the stock boundaries, with or without a theme block', () => {
    expect(FrznforgeConfigSchema.parse(base).theme.heat).toEqual({ hot: 7, warm: 30, neutral: 180, cool: 365 });
    expect(FrznforgeConfigSchema.parse({ ...base, theme: { palette: 'frost' } }).theme.heat).toEqual({
      hot: 7,
      warm: 30,
      neutral: 180,
      cool: 365,
    });
  });

  it('keeps configured boundaries and fills the rest with defaults', () => {
    const cfg = FrznforgeConfigSchema.parse({ ...base, theme: { heat: { hot: 3 } } });
    expect(cfg.theme.heat).toEqual({ hot: 3, warm: 30, neutral: 180, cool: 365 });
  });

  it('rejects non-ascending boundaries', () => {
    expect(() => FrznforgeConfigSchema.parse({ ...base, theme: { heat: { hot: 40 } } })).toThrow(/ascending/);
    expect(() => FrznforgeConfigSchema.parse({ ...base, theme: { heat: { warm: 400 } } })).toThrow(/ascending/);
    expect(() =>
      FrznforgeConfigSchema.parse({ ...base, theme: { heat: { hot: 10, warm: 10, neutral: 20, cool: 30 } } }),
    ).toThrow(/ascending/);
  });

  it('rejects non-positive and fractional boundaries', () => {
    expect(() => FrznforgeConfigSchema.parse({ ...base, theme: { heat: { hot: 0 } } })).toThrow();
    expect(() => FrznforgeConfigSchema.parse({ ...base, theme: { heat: { hot: -1 } } })).toThrow();
    expect(() => FrznforgeConfigSchema.parse({ ...base, theme: { heat: { hot: 1.5 } } })).toThrow();
  });
});

describe('ingest.reuse', () => {
  it('defaults to enabled with a 2-minute window, and both 0.3.0 skips OFF', () => {
    // skipUnchanged and cooldownSeconds are opt-in by design: they trade a guarantee of
    // freshness for speed, which is the user's call, not a default.
    expect(FrznforgeConfigSchema.parse(base).ingest.reuse).toEqual({
      enabled: true,
      maxAgeMinutes: 2,
      skipUnchanged: false,
      cooldownSeconds: null,
    });
  });

  it('accepts overrides and rejects a non-positive window', () => {
    expect(
      FrznforgeConfigSchema.parse({ ...base, ingest: { reuse: { enabled: false, maxAgeMinutes: 10 } } }).ingest.reuse,
    ).toMatchObject({ enabled: false, maxAgeMinutes: 10 });
    expect(() => FrznforgeConfigSchema.parse({ ...base, ingest: { reuse: { maxAgeMinutes: 0 } } })).toThrow();
  });

  it('validates the 0.3.0 refetch knobs', () => {
    const reuse = (r: unknown) => FrznforgeConfigSchema.parse({ ...base, ingest: { reuse: r } }).ingest.reuse;
    expect(reuse({ skipUnchanged: true, cooldownSeconds: 3600 })).toMatchObject({
      skipUnchanged: true,
      cooldownSeconds: 3600,
    });
    expect(reuse({ cooldownSeconds: 0 }).cooldownSeconds).toBe(0); // "no cooldown", explicitly
    expect(reuse({ cooldownSeconds: null }).cooldownSeconds).toBeNull();
    expect(() => reuse({ cooldownSeconds: -1 })).toThrow();
    expect(() => reuse({ cooldownSeconds: 1.5 })).toThrow();
    expect(() => reuse({ skipUnchanged: 'yes' })).toThrow();
  });
});

describe('ingest.failOnDegraded', () => {
  it('defaults to off, so a rate-limited build still succeeds', () => {
    expect(FrznforgeConfigSchema.parse(base).ingest.failOnDegraded).toBe(false);
    expect(FrznforgeConfigSchema.parse({ ...base, ingest: { failOnDegraded: true } }).ingest.failOnDegraded).toBe(true);
    expect(() => FrznforgeConfigSchema.parse({ ...base, ingest: { failOnDegraded: 'yes' } })).toThrow();
  });
});

describe('ingest.maxCommitAgeDays', () => {
  it('defaults to null (no limit) and accepts positive integers', () => {
    expect(FrznforgeConfigSchema.parse(base).ingest.maxCommitAgeDays).toBeNull();
    expect(
      FrznforgeConfigSchema.parse({ ...base, ingest: { maxCommitAgeDays: 30 } }).ingest.maxCommitAgeDays,
    ).toBe(30);
  });

  it('rejects zero, negatives and fractions', () => {
    for (const bad of [0, -5, 1.5]) {
      expect(() => FrznforgeConfigSchema.parse({ ...base, ingest: { maxCommitAgeDays: bad } })).toThrow();
    }
  });
});
