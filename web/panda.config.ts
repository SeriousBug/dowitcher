import { defineConfig } from "@pandacss/dev";

/**
 * Dowitcher is dark-first on purpose: people read comics at night, and every
 * screen here frames artwork. The neutrals are warm — a red-brown cast rather
 * than the usual blue-grey — so they read as cardboard and backing board in a
 * dim room instead of as a server dashboard, and so a page of yellowed newsprint
 * doesn't look jaundiced next to them.
 *
 * The chrome deliberately holds no colour of its own. Cover art is the only
 * saturated thing on the library grid, and anything competing with it is a bug.
 * The single accent is process magenta, the ink that makes comics look like
 * comics: it appears on what you can act on, and nowhere else.
 */
export default defineConfig({
  preflight: true,
  include: ["./src/**/*.{js,jsx,ts,tsx}"],
  exclude: [],
  jsxFramework: "react",
  outdir: "styled-system",

  theme: {
    extend: {
      tokens: {
        colors: {
          // Warm neutrals, black to white. The whole shell is built from these.
          ink: {
            950: { value: "#0a0809" },
            900: { value: "#131011" },
            850: { value: "#1a1618" },
            800: { value: "#221d1f" },
            750: { value: "#2b2427" },
            700: { value: "#362e31" },
            600: { value: "#4b4145" },
            500: { value: "#6d6165" },
            400: { value: "#8b7f83" },
            300: { value: "#a99ea2" },
            200: { value: "#c9c1c4" },
            100: { value: "#e6e1e2" },
            50: { value: "#f6f3f4" },
          },
          // Process magenta — the M of CMYK, straight off a four-colour press.
          magenta: {
            950: { value: "#2b0b1d" },
            900: { value: "#4a0f30" },
            700: { value: "#9e0d56" },
            600: { value: "#c9126e" },
            500: { value: "#e91e84" },
            400: { value: "#f95ba6" },
            300: { value: "#ff8ec4" },
          },
          // Status inks. Muted against the accent so a green tick never outshouts
          // the one thing on screen that is actually clickable.
          verdigris: {
            600: { value: "#1f9463" },
            500: { value: "#35c184" },
            300: { value: "#7fdcb0" },
          },
          amber: {
            600: { value: "#c07d18" },
            500: { value: "#f0a536" },
            300: { value: "#f8cd84" },
          },
          rust: {
            700: { value: "#8f2222" },
            600: { value: "#c93636" },
            500: { value: "#ef4b4b" },
            300: { value: "#f79a9a" },
          },
        },
        fonts: {
          // No web fonts: this ships as one offline binary, and a library of
          // covers is not worth a font payload. The stack leans on the widest
          // grotesques a machine is likely to already have.
          body: {
            value:
              "'Inter', 'Segoe UI Variable', 'Segoe UI', system-ui, -apple-system, sans-serif",
          },
          heading: {
            value:
              "'Archivo', 'Inter Tight', 'Inter', 'Segoe UI', system-ui, -apple-system, sans-serif",
          },
          mono: {
            value: "ui-monospace, 'SF Mono', 'Cascadia Mono', Menlo, monospace",
          },
        },
        radii: {
          // Comics are rectangles in a rectangular box. Corners stay crisp;
          // a cover with a soft radius looks like a phone app icon.
          sm: { value: "3px" },
          md: { value: "5px" },
          lg: { value: "8px" },
          xl: { value: "12px" },
          full: { value: "9999px" },
        },
        shadows: {
          // Drop shadow does almost nothing on near-black, so depth comes from a
          // hairline highlight on top plus a wide black pool underneath.
          card: {
            value:
              "inset 0 1px 0 0 rgba(255, 255, 255, 0.04), 0 2px 4px -2px rgba(0, 0, 0, 0.7), 0 12px 28px -16px rgba(0, 0, 0, 0.9)",
          },
          pop: {
            value:
              "inset 0 1px 0 0 rgba(255, 255, 255, 0.05), 0 28px 64px -20px rgba(0, 0, 0, 0.92)",
          },
          // Cover art supplies its own edges, so it gets a pool and nothing else.
          cover: { value: "0 10px 30px -12px rgba(0, 0, 0, 0.95)" },
          glow: { value: "0 0 0 1px {colors.magenta.500}, 0 0 22px -4px {colors.magenta.600}" },
        },
        sizes: {
          // Standard US comic trim, 6.625" × 10.1875". Every cover slot and every
          // skeleton uses it so the grid never reflows once art loads.
          cover: { value: "0.65" },
        },
      },
      semanticTokens: {
        colors: {
          accent: { value: "{colors.magenta.500}" },
          accentHover: { value: "{colors.magenta.400}" },
          accentQuiet: { value: "{colors.magenta.950}" },
          bg: { value: "{colors.ink.900}" },
          surface: { value: "{colors.ink.850}" },
          surfaceRaised: { value: "{colors.ink.800}" },
          border: { value: "{colors.ink.700}" },
          text: { value: "{colors.ink.50}" },
          textMuted: { value: "{colors.ink.400}" },
          ok: { value: "{colors.verdigris.500}" },
          attention: { value: "{colors.amber.500}" },
          danger: { value: "{colors.rust.500}" },
          dangerHover: { value: "{colors.rust.600}" },
          // Darker than bg: the reader is a lightbox, and the page has to be the
          // brightest thing in the room with nothing around it to measure against.
          reader: { value: "{colors.ink.950}" },
        },
      },
      keyframes: {
        spin: {
          to: { transform: "rotate(360deg)" },
        },
        // Skeleton covers idle rather than pulse — a wall of them blinking in
        // unison while a library scans is unbearable.
        shimmer: {
          "0%, 100%": { opacity: "0.5" },
          "50%": { opacity: "0.75" },
        },
      },
    },
  },

  globalCss: {
    body: {
      fontFamily: "body",
      bg: "bg",
      color: "text",
      // Rendered against near-black, default weights bloom and look bolder than
      // they are; grayscale AA pulls them back to their real weight.
      WebkitFontSmoothing: "antialiased",
    },
    "h1, h2, h3": {
      fontFamily: "heading",
      letterSpacing: "-0.02em",
    },
    "::selection": {
      bg: "magenta.500",
      color: "white",
    },
    // One focus treatment everywhere, on the accent, so keyboard users get the
    // same signal the mouse gets on hover.
    ":focus-visible": {
      outline: "2px solid token(colors.accent)",
      outlineOffset: "2px",
    },
  },
});
