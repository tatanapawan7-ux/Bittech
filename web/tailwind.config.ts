import type { Config } from "tailwindcss";
const config: Config = {
  content: ["./app/**/*.{ts,tsx}", "./components/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        up: "#16c784",
        down: "#ea3943",
        panel: "#161a1e",
        panel2: "#1e2329",
        line: "#2b3139",
      },
    },
  },
  plugins: [],
};
export default config;
