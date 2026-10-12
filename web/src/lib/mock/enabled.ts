// Mock switch. A public variable injected at build time (only NEXT_PUBLIC_-prefixed vars are readable in the browser).
// Set NEXT_PUBLIC_MOCK=1 on Vercel to run the whole site on mock data, no backend needed.
export const MOCK = process.env.NEXT_PUBLIC_MOCK === "1";
