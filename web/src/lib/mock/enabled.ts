// Mock toggle. Build-time public env var (only NEXT_PUBLIC_ prefix is readable in the browser).
// Setting NEXT_PUBLIC_MOCK=1 on Vercel runs the entire site on mock data without a backend.
export const MOCK = process.env.NEXT_PUBLIC_MOCK === "1";
