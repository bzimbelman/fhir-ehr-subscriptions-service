import type { Config } from "tailwindcss";

// Tailwind 4 takes most of its configuration from CSS (@theme directives in
// globals.css). The config file is kept as the canonical place to extend
// content globs and plugin lists if/when we need them.
const config: Config = {
  content: ["./src/**/*.{ts,tsx}"],
  theme: {
    extend: {},
  },
  plugins: [],
};

export default config;
