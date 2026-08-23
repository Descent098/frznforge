// frznforge site configuration.
// Read at build time (astro build / astro dev) — changing it requires a rebuild.
// This file will grow into the full site config described in docs/dev/plans/plan-phases.md (Phase 1).

export type Palette = 'hearth' | 'frost';

export interface FrznforgeConfig {
  theme: {
    /**
     * Colour palette for the site. Layout and components are identical; only colours change.
     *  - 'hearth' — warm: off-white / ember-tinted charcoal canvas, ember primary.
     *  - 'frost'  — cool: slate-grey / blue-tinted navy canvas, ice-leaning neutrals.
     */
    palette: Palette;
  };
}

const config: FrznforgeConfig = {
  theme: {
    palette: 'hearth',
  },
};

export default config;
