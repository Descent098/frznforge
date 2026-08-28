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
  it('defaults to enabled with a 2-minute window', () => {
    expect(FrznforgeConfigSchema.parse(base).ingest.reuse).toEqual({ enabled: true, maxAgeMinutes: 2 });
  });

  it('accepts overrides and rejects a non-positive window', () => {
    expect(
      FrznforgeConfigSchema.parse({ ...base, ingest: { reuse: { enabled: false, maxAgeMinutes: 10 } } }).ingest.reuse,
    ).toEqual({ enabled: false, maxAgeMinutes: 10 });
    expect(() => FrznforgeConfigSchema.parse({ ...base, ingest: { reuse: { maxAgeMinutes: 0 } } })).toThrow();
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
