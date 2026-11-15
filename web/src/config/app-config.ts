import packageJson from "../../package.json";

const currentYear = new Date().getFullYear();

export const APP_CONFIG = {
  name: "VMT-23",
  version: packageJson.version,
  copyright: `© ${currentYear}, VMT-23.`,
  meta: {
    title: "VMT-23 — Autonomous Penetration Testing Console",
    description: "LLM-powered autonomous penetration testing system console",
  },
};
