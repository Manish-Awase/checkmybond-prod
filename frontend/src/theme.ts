// frontend/src/theme.ts
//
// Shared design tokens. Deliberately NOT the generic SaaS defaults --
// see the design rationale in LandingPage.tsx's leading comment for why
// these specific choices (cool paper instead of warm cream, serif
// display type, emerald/rust instead of blue/red). Pages should import
// from here rather than redefining their own color literals, so a
// future palette change is one file, not a grep-and-replace across
// every page.
export function theme(dark: boolean) {
  return {
    ink:     dark ? "#EDEFEA" : "#12151C",
    paper:   dark ? "#12151C" : "#EEF0EC",
    surface: dark ? "#1B1F27" : "#FFFFFF",
    line:    dark ? "#2A2F38" : "#D8DCD5",
    muted:   dark ? "#9BA39B" : "#5B6157",
    verified:"#0E6E55", // emerald -- matched fields, confirmed state
    flag:    "#9C4221", // burnt rust -- mismatches, needs-review state
    accent:  "#0E6E55",
  }
}

export const FONT_DISPLAY = "'Source Serif 4', Georgia, serif"
export const FONT_BODY    = "'IBM Plex Sans', -apple-system, sans-serif"

export const FONT_IMPORT = `
  @import url('https://fonts.googleapis.com/css2?family=Source+Serif+4:opsz,wght@8..60,400;8..60,600;8..60,700&family=IBM+Plex+Sans:wght@400;500;600;700&display=swap');
`
